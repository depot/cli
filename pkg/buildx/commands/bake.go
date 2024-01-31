// Source: https://github.com/docker/buildx/blob/v0.10/commands/bake.go

package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/containerd/containerd/platforms"
	"github.com/depot/cli/pkg/buildx/bake"
	"github.com/depot/cli/pkg/buildx/build"
	"github.com/depot/cli/pkg/buildx/builder"
	"github.com/depot/cli/pkg/compose"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/load"
	depotprogress "github.com/depot/cli/pkg/progress"
	"github.com/depot/cli/pkg/registry"
	"github.com/depot/cli/pkg/sbom"
	buildx "github.com/docker/buildx/build"
	"github.com/docker/buildx/util/buildflags"
	"github.com/docker/buildx/util/confutil"
	"github.com/docker/buildx/util/dockerutil"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/buildx/util/tracing"
	"github.com/docker/cli/cli/command"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"golang.org/x/exp/maps"
	"golang.org/x/sync/errgroup"
)

type BakeOptions struct {
	files     []string
	overrides []string
	printOnly bool
	commonOptions
	DepotOptions
}

func RunBake(dockerCli command.Cli, in BakeOptions, validator BakeValidator) (err error) {
	ctx := appcontext.Context()

	ctx, end, err := tracing.TraceCurrentCommand(ctx, "bake")
	if err != nil {
		return err
	}
	defer func() {
		end(err)
	}()

	ctx2, cancel := context.WithCancel(context.TODO())

	buildxprinter, err := progress.NewPrinter(ctx2, os.Stderr, os.Stderr, in.progress)
	if err != nil {
		cancel()
		return err
	}

	printer, finish, err := depotprogress.NewProgress(ctx2, in.buildID, in.token, buildxprinter)
	if err != nil {
		cancel()
		return err
	}

	defer func() {
		// There is extra logic far below that will also do a printer.Wait()
		// if there are no errors.  We want to control when the buildx printer
		// finishes writing so that we can write our own information such as
		// linting without it being interleaved.
		if printer != nil && err != nil {
			err1 := printer.Wait()
			if err == nil && !errors.Is(err1, context.Canceled) {
				err = err1
			}
		}
	}()

	defer finish() // Required to ensure that the printer is stopped before the context is cancelled.
	defer cancel()

	if os.Getenv("DEPOT_NO_SUMMARY_LINK") == "" {
		progress.Write(printer, "[depot] build: "+in.buildURL, func() error { return err })
	}

	contextPathHash, _ := os.Getwd()
	builderOpts := append([]builder.Option{builder.WithName(in.builder),
		builder.WithContextPathHash(contextPathHash)}, in.builderOptions...)
	b, err := builder.New(dockerCli, builderOpts...)
	if err != nil {
		return err
	}
	if err = updateLastActivity(dockerCli, b.NodeGroup); err != nil {
		return errors.Wrapf(err, "failed to update builder last activity time")
	}
	nodes, err := b.LoadNodes(ctx, false)
	if err != nil {
		return err
	}

	buildOpts, err := validator.Validate(ctx, nodes, printer)
	if err != nil {
		return err
	}

	var (
		pullOpts map[string]load.PullOptions
		// Only used for failures to pull images.
		fallbackOpts map[string]buildx.Options
	)
	if in.exportLoad {
		fallbackOpts = maps.Clone(buildOpts)
		buildOpts, pullOpts = load.WithDepotImagePull(
			buildOpts,
			load.DepotLoadOptions{
				UseLocalRegistry: in.DepotOptions.useLocalRegistry,
				Project:          in.DepotOptions.project,
				BuildID:          in.DepotOptions.buildID,
				IsBake:           true,
				ProgressMode:     in.progress,
			},
		)
	}
	if in.save {
		opts := registry.SaveOptions{
			ProjectID:             in.project,
			BuildID:               in.buildID,
			AdditionalTags:        in.additionalTags,
			AdditionalCredentials: in.additionalCredentials,
		}
		buildOpts = registry.WithDepotSave(buildOpts, opts)
	}

	buildxNodes := builder.ToBuildxNodes(nodes)
	buildxNodes, err = build.FilterAvailableNodes(buildxNodes)
	if err != nil {
		return wrapBuildError(err, true)
	}

	dockerClient := dockerutil.NewClient(dockerCli)
	dockerConfigDir := confutil.ConfigDir(dockerCli)
	buildxopts := build.BuildxOpts(buildOpts)

	// "Boot" the depot nodes.
	_, clients, err := build.ResolveDrivers(ctx, buildxNodes, buildxopts, printer)
	if err != nil {
		return wrapBuildError(err, true)
	}

	linter := NewLinter(printer, NewLintFailureMode(in.lint, in.lintFailOn), clients, buildxNodes)
	resp, err := build.DepotBuild(ctx, buildxNodes, buildOpts, dockerClient, dockerConfigDir, buildxprinter, linter, in.DepotOptions.build)
	if err != nil {
		if errors.Is(err, LintFailed) {
			linter.Print(os.Stderr, in.progress)
		}
		return wrapBuildError(err, true)
	}

	if in.metadataFile != "" {
		dt := make(map[string]interface{})
		for _, buildRes := range resp {
			metadata := map[string]interface{}{}
			for _, nodeRes := range buildRes.NodeResponses {
				nodeMetadata := decodeExporterResponse(nodeRes.SolveResponse.ExporterResponse)
				for k, v := range nodeMetadata {
					metadata[k] = v
				}
			}
			dt[buildRes.Name] = metadata
		}
		if err := writeMetadataFile(in.metadataFile, in.project, in.buildID, dt); err != nil {
			return err
		}
	}

	if in.sbomDir != "" {
		err := sbom.Save(ctx, in.sbomDir, resp)
		if err != nil {
			return err
		}
	}

	if len(pullOpts) > 0 {
		eg, ctx2 := errgroup.WithContext(ctx)
		// Three concurrent pulls at a time to avoid overwhelming the registry.
		eg.SetLimit(3)
		for i := range resp {
			func(i int) {
				eg.Go(func() error {
					depotResponses := []build.DepotBuildResponse{resp[i]}
					err := load.DepotFastLoad(ctx2, dockerCli.Client(), depotResponses, pullOpts, printer)
					load.DeleteExportLeases(ctx2, depotResponses)
					return err
				})
			}(i)
		}

		err := eg.Wait()
		if err != nil && !errors.Is(err, context.Canceled) {
			// For now, we will fallback by rebuilding with load.
			if in.exportLoad {
				progress.Write(printer, "[load] fast load failed; retrying", func() error { return err })
				buildOpts, _ = load.WithDepotImagePull(fallbackOpts, load.DepotLoadOptions{})
				_, err = build.DepotBuild(ctx, buildxNodes, buildOpts, dockerClient, dockerConfigDir, printer, nil, in.DepotOptions.build)
			}

			return err
		}
	}

	_ = printer.Wait()
	linter.Print(os.Stderr, in.progress)
	return nil
}

func BakeCmd(dockerCli command.Cli) *cobra.Command {
	var options BakeOptions

	cmd := &cobra.Command{
		Use:     "bake [OPTIONS] [TARGET...]",
		Aliases: []string{"f"},
		Short:   "Build from a file",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: remove when upgrading to buildx 0.12
			for idx, file := range options.files {
				if strings.HasPrefix(file, "cwd://") {
					options.files[idx] = strings.TrimPrefix(file, "cwd://")
				}
			}

			if options.printOnly {
				if isRemoteTarget(args) {
					return errors.New("cannot use remote target with --print")
				}
				return BakePrint(dockerCli, args, options)
			}

			// reset to nil to avoid override is unset
			if !cmd.Flags().Lookup("no-cache").Changed {
				options.noCache = nil
			}
			if !cmd.Flags().Lookup("pull").Changed {
				options.pull = nil
			}

			token, err := helpers.ResolveToken(context.Background(), options.token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			options.project = helpers.ResolveProjectID(options.project, options.files...)

			buildPlatform, err := helpers.ResolveBuildPlatform(options.buildPlatform)
			if err != nil {
				return err
			}

			var (
				validator     BakeValidator
				validatedOpts map[string]buildx.Options
			)
			if isRemoteTarget(args) {
				validator = NewRemoteBakeValidator(options, args)
			} else {
				validator = NewLocalBakeValidator(options, args)
				// Parse the local bake file before starting the build to catch errors early.
				validatedOpts, err = validator.Validate(context.Background(), nil, nil)
				if err != nil {
					return err
				}
			}

			req := helpers.NewBakeRequest(
				options.project,
				validatedOpts,
				helpers.UsingDepotFeatures{
					Push: options.exportPush,
					Load: options.exportLoad,
					Save: options.save,
					Lint: options.lint,
				},
			)
			build, err := helpers.BeginBuild(context.Background(), req, token)
			if err != nil {
				return err
			}
			var buildErr error
			defer func() {
				build.Finish(buildErr)
				PrintBuildURL(build.BuildURL, options.progress)
			}()

			options.builderOptions = []builder.Option{builder.WithDepotOptions(buildPlatform, build)}

			buildProject := build.BuildProject()
			if buildProject != "" {
				options.project = buildProject
			}
			if options.save {
				options.additionalCredentials = build.AdditionalCredentials()
				options.additionalTags = build.AdditionalTags()
			}
			options.buildID = build.ID
			options.buildURL = build.BuildURL
			options.token = build.Token
			options.useLocalRegistry = build.UseLocalRegistry
			options.proxyImage = build.ProxyImage
			options.build = &build

			if options.allowNoOutput {
				_ = os.Setenv("BUILDX_NO_DEFAULT_LOAD", "1")
			}

			buildErr = retryRetryableErrors(context.Background(), func() error {
				return RunBake(dockerCli, options, validator)
			})
			return rewriteFriendlyErrors(buildErr)
		},
	}

	flags := cmd.Flags()

	flags.StringArrayVarP(&options.files, "file", "f", []string{}, "Build definition file")
	flags.BoolVar(&options.exportLoad, "load", false, `Shorthand for "--set=*.output=type=docker"`)
	flags.BoolVar(&options.printOnly, "print", false, "Print the options without building")
	flags.BoolVar(&options.exportPush, "push", false, `Shorthand for "--set=*.output=type=registry"`)
	flags.StringVar(&options.sbom, "sbom", "", `Shorthand for "--set=*.attest=type=sbom"`)
	flags.StringVar(&options.provenance, "provenance", "", `Shorthand for "--set=*.attest=type=provenance"`)
	flags.StringArrayVar(&options.overrides, "set", nil, `Override target value (e.g., "targetpattern.key=value")`)

	commonBuildFlags(&options.commonOptions, flags)
	depotFlags(cmd, &options.DepotOptions, flags)

	return cmd
}

func overrides(in BakeOptions) []string {
	overrides := in.overrides
	if in.exportPush {
		overrides = append(overrides, "*.push=true")
	}

	if in.noCache != nil {
		overrides = append(overrides, fmt.Sprintf("*.no-cache=%t", *in.noCache))
	}
	if in.pull != nil {
		overrides = append(overrides, fmt.Sprintf("*.pull=%t", *in.pull))
	}
	if in.sbom != "" {
		overrides = append(overrides, fmt.Sprintf("*.attest=%s", buildflags.CanonicalizeAttest("sbom", in.sbom)))
	}
	if in.provenance != "" {
		overrides = append(overrides, fmt.Sprintf("*.attest=%s", buildflags.CanonicalizeAttest("provenance", in.provenance)))
	}
	return overrides
}

func isRemoteTarget(targets []string) bool {
	if len(targets) == 0 {
		return false
	}

	return bake.IsRemoteURL(targets[0])
}

var (
	_ BakeValidator = (*RemoteBakeValidator)(nil)
	_ BakeValidator = (*LocalBakeValidator)(nil)
)

// BakeValidator returns either local or remote build options for targets.
type BakeValidator interface {
	Validate(ctx context.Context, nodes []builder.Node, pw progress.Writer) (map[string]buildx.Options, error)
}

type LocalBakeValidator struct {
	options     BakeOptions
	bakeTargets bakeTargets

	once      sync.Once
	buildOpts map[string]buildx.Options
	err       error
}

func NewLocalBakeValidator(options BakeOptions, args []string) *LocalBakeValidator {
	return &LocalBakeValidator{
		options:     options,
		bakeTargets: parseBakeTargets(args),
	}
}

func (t *LocalBakeValidator) Validate(ctx context.Context, _ []builder.Node, _ progress.Writer) (map[string]buildx.Options, error) {
	// Using a sync.Once because I _think_ the bake file may not always be read
	// more than one time such as passed over stdin.
	t.once.Do(func() {
		files, err := bake.ReadLocalFiles(t.options.files)
		if err != nil {
			t.err = err
			return
		}

		overrides := overrides(t.options)
		defaults := map[string]string{
			"BAKE_CMD_CONTEXT":    t.bakeTargets.CmdContext,
			"BAKE_LOCAL_PLATFORM": platforms.DefaultString(),
		}

		targets, _, err := bake.ReadTargets(ctx, files, t.bakeTargets.Targets, overrides, defaults)
		if err != nil {
			t.err = err
			return
		}

		tags, err := compose.TargetTags(files)
		if err != nil {
			t.err = err
			return
		}

		for target, opts := range targets {
			if tag, ok := tags[target]; ok {
				opts.Tags = tag
			}
		}

		t.buildOpts, t.err = bake.TargetsToBuildOpt(targets, nil)
	})

	return t.buildOpts, t.err
}

type RemoteBakeValidator struct {
	options     BakeOptions
	bakeTargets bakeTargets
}

func NewRemoteBakeValidator(options BakeOptions, args []string) *RemoteBakeValidator {
	return &RemoteBakeValidator{
		options:     options,
		bakeTargets: parseBakeTargets(args),
	}
}

func (t *RemoteBakeValidator) Validate(ctx context.Context, nodes []builder.Node, pw progress.Writer) (map[string]buildx.Options, error) {
	files, inp, err := bake.ReadRemoteFiles(ctx, builder.ToBuildxNodes(nodes), t.bakeTargets.FileURL, t.options.files, pw)
	if err != nil {
		return nil, err
	}

	overrides := overrides(t.options)
	defaults := map[string]string{
		"BAKE_CMD_CONTEXT":    t.bakeTargets.CmdContext,
		"BAKE_LOCAL_PLATFORM": platforms.DefaultString(),
	}

	targets, _, err := bake.ReadTargets(ctx, files, t.bakeTargets.Targets, overrides, defaults)
	if err != nil {
		return nil, err
	}

	return bake.TargetsToBuildOpt(targets, inp)
}

type bakeTargets struct {
	CmdContext string
	FileURL    string
	Targets    []string
}

// parseBakeTargets parses the command-line arguments (aka targets).
func parseBakeTargets(targets []string) (bkt bakeTargets) {
	bkt.CmdContext = "cwd://"

	if len(targets) > 0 {
		if bake.IsRemoteURL(targets[0]) {
			bkt.FileURL = targets[0]
			targets = targets[1:]
			if len(targets) > 0 {
				if bake.IsRemoteURL(targets[0]) {
					bkt.CmdContext = targets[0]
					targets = targets[1:]
				}
			}
		}
	}

	if len(targets) == 0 {
		targets = []string{"default"}
	}

	bkt.Targets = targets
	return bkt
}
