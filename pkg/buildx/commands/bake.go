// Source: https://github.com/docker/buildx/blob/v0.10/commands/bake.go

package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/containerd/containerd/platforms"
	depotbuild "github.com/depot/cli/pkg/build"
	"github.com/depot/cli/pkg/buildx/bake"
	"github.com/depot/cli/pkg/buildx/build"
	"github.com/depot/cli/pkg/buildx/builder"
	"github.com/depot/cli/pkg/compose"
	"github.com/depot/cli/pkg/dockerclient"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/load"
	"github.com/depot/cli/pkg/progresshelper"
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
	"golang.org/x/exp/slices"
	"golang.org/x/sync/errgroup"
)

type BakeOptions struct {
	files     []string
	overrides []string
	printOnly bool
	commonOptions
	DepotOptions
}

func RunBake(dockerCli command.Cli, in BakeOptions, validator BakeValidator, printer *progresshelper.SharedPrinter) (linter *Linter, requestedTargets []string, err error) {
	ctx := appcontext.Context()

	ctx, end, err := tracing.TraceCurrentCommand(ctx, "bake")
	if err != nil {
		return nil, nil, wrapBuildError(err, true)
	}
	defer func() {
		end(err)
	}()

	if os.Getenv("DEPOT_NO_SUMMARY_LINK") == "" {
		progress.Write(printer, "[depot] build: "+in.buildURL, func() error { return err })
	}

	contextPathHash, _ := os.Getwd()
	builderOpts := append([]builder.Option{builder.WithName(in.builder),
		builder.WithContextPathHash(contextPathHash)}, in.builderOptions...)
	b, err := builder.New(dockerCli, builderOpts...)
	if err != nil {
		return nil, nil, err
	}
	if err = updateLastActivity(dockerCli, b.NodeGroup); err != nil {
		return nil, nil, errors.Wrapf(err, "failed to update builder last activity time")
	}
	nodes, err := b.LoadNodes(ctx, false)
	if err != nil {
		return nil, nil, err
	}

	validatedOpts, requestedTargets, err := validator.Validate(ctx, nodes, printer)
	if err != nil {
		return nil, nil, err
	}

	buildOpts := validatedOpts.ProjectOpts(in.project)
	if buildOpts == nil {
		return nil, nil, fmt.Errorf("project %s build options not found", in.project)
	}

	// Filter requestedTargets to only include targets for the current project
	projectRequestedTargets := make([]string, 0)
	for _, target := range requestedTargets {
		if _, exists := buildOpts[target]; exists {
			projectRequestedTargets = append(projectRequestedTargets, target)
		}
	}
	requestedTargets = projectRequestedTargets

	targetsToLoad := make([]string, 0)
	for target, opts := range buildOpts {
		shouldLoad := true
		for _, export := range opts.Exports {
			if export.Type == "cacheonly" {
				shouldLoad = false
				break
			}
		}

		// When using --load, only load originally requested targets, not dependencies
		if in.exportLoad {
			shouldLoad = shouldLoad && slices.Contains(requestedTargets, target)
		}

		if shouldLoad {
			targetsToLoad = append(targetsToLoad, target)
		}
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
				Project:       in.DepotOptions.project,
				BuildID:       in.DepotOptions.buildID,
				IsBake:        true,
				ProgressMode:  in.progress,
				UseRegistry:   in.DepotOptions.loadUsingRegistry,
				PullInfo:      in.DepotOptions.pullInfo,
				BuildPlatform: in.DepotOptions.buildPlatform,
			},
		)
	}
	if in.save {
		opts := registry.SaveOptions{
			ProjectID:             in.project,
			BuildID:               in.buildID,
			AdditionalTags:        in.additionalTags,
			AdditionalCredentials: in.additionalCredentials,
			AddTargetSuffix:       true,
			RequestedTargets:      requestedTargets,
		}
		buildOpts = registry.WithDepotSave(buildOpts, opts)
	}

	buildxNodes := builder.ToBuildxNodes(nodes)
	buildxNodes, err = build.FilterAvailableNodes(buildxNodes)
	if err != nil {
		return nil, nil, wrapBuildError(err, true)
	}

	dockerClient := dockerutil.NewClient(dockerCli)
	dockerConfigDir := confutil.ConfigDir(dockerCli)
	buildxopts := build.BuildxOpts(buildOpts)

	// "Boot" the depot nodes.
	_, clients, err := build.ResolveDrivers(ctx, buildxNodes, buildxopts, printer)
	if err != nil {
		return nil, nil, wrapBuildError(err, true)
	}

	linter = NewLinter(printer, NewLintFailureMode(in.lint, in.lintFailOn), clients, buildxNodes)
	resp, err := build.DepotBuild(ctx, buildxNodes, buildOpts, dockerClient, dockerConfigDir, printer, linter, in.DepotOptions.build)
	if err != nil {
		if errors.Is(err, LintFailed) {
			linter.Print(os.Stderr, in.progress)
		}
		return nil, nil, wrapBuildError(err, true)
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
			// Only include targets that have metadata (i.e., were exported)
			if len(metadata) > 0 {
				dt[buildRes.Name] = metadata
			}
		}
		err = writeMetadataFile(in.metadataFile, in.project, in.buildID, requestedTargets, dt, true)
		if err != nil {
			return nil, nil, err
		}
	}

	if in.sbomDir != "" {
		err = sbom.Save(ctx, in.sbomDir, resp)
		if err != nil {
			return nil, nil, err
		}
	}

	if len(pullOpts) > 0 {
		eg, ctx2 := errgroup.WithContext(ctx)
		// Three concurrent pulls at a time to avoid overwhelming the registry.
		eg.SetLimit(3)
		for i := range resp {
			eg.Go(func() error {
				depotResponses := []build.DepotBuildResponse{resp[i]}
				var err error
				// Only load images from requested targets to avoid pulling unnecessary images.
				if slices.Contains(targetsToLoad, resp[i].Name) {
					reportingPrinter := progresshelper.NewReporter(ctx2, printer, in.buildID, in.token)
					defer reportingPrinter.Close()

					if in.DepotOptions.loadUsingRegistry && in.DepotOptions.pullInfo != nil {
						target := resp[i].Name
						pullOpt, ok := pullOpts[target]
						if ok {
							pw := progress.WithPrefix(reportingPrinter, target, len(pullOpts) > 1)
							err = load.PullImages(ctx, dockerCli.Client(), fmt.Sprintf("%s-%s", in.DepotOptions.pullInfo.Reference, target), pullOpt, pw)
						}
					} else {
						err = load.DepotFastLoad(ctx2, dockerCli.Client(), depotResponses, pullOpts, reportingPrinter)
					}
				}
				load.DeleteExportLeases(ctx2, depotResponses)
				return err
			})
		}

		err = eg.Wait()
		if err != nil && !errors.Is(err, context.Canceled) {
			// For now, we will fallback by rebuilding with load.
			if in.exportLoad {
				progress.Write(printer, "[load] fast load failed; retrying", func() error { return err })
				buildOpts = load.WithSelectiveDockerLoad(fallbackOpts, targetsToLoad)
				_, err = build.DepotBuild(ctx, buildxNodes, buildOpts, dockerClient, dockerConfigDir, printer, nil, in.DepotOptions.build)
			}

			return nil, nil, err
		}
	}

	return linter, requestedTargets, nil
}

func BakeCmd() *cobra.Command {
	var options BakeOptions

	cmd := &cobra.Command{
		Use:     "bake [OPTIONS] [TARGET...]",
		Aliases: []string{"f"},
		Short:   "Build from a file",
		RunE: func(cmd *cobra.Command, args []string) error {
			dockerCli, err := dockerclient.NewDockerCLI()
			if err != nil {
				return err
			}

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

			token, err := helpers.ResolveProjectAuth(context.Background(), options.token)
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
				validatedOpts *bake.DepotBakeOptions
			)
			if isRemoteTarget(args) {
				validator = NewRemoteBakeValidator(options, args)
			} else {
				validator = NewLocalBakeValidator(options, args)
				// Parse the local bake file before starting the build to catch errors early.
				validatedOpts, _, err = validator.Validate(context.Background(), nil, nil)
				if err != nil {
					return err
				}
			}

			projectIDs := validatedOpts.ProjectIDs()

			printer, err := progresshelper.NewSharedPrinter(options.progress)
			if err != nil {
				return err
			}

			for range projectIDs {
				printer.Add()
			}

			type saveInfo struct {
				project        string
				buildID        string
				additionalTags []string
			}
			type buildResult struct {
				linter           *Linter
				requestedTargets []string
				saveInfo         *saveInfo
			}
			var mu sync.Mutex
			var buildResults []buildResult

			eg, ctx := errgroup.WithContext(context.Background())
			for _, projectID := range projectIDs {
				options.project = projectID
				bakeOpts := validatedOpts.ProjectOpts(projectID)

				req := helpers.NewBakeRequest(
					options.project,
					bakeOpts,
					helpers.UsingDepotFeatures{
						Push:     options.exportPush,
						Load:     options.exportLoad,
						Save:     options.save,
						SaveTags: options.saveTags,
						Lint:     options.lint,
					},
				)
				build, err := helpers.BeginBuild(context.Background(), req, token)
				if err != nil {
					return err
				}
				options.builderOptions = []builder.Option{builder.WithDepotOptions(buildPlatform, build)}

				buildProject := build.BuildProject()
				if buildProject != "" {
					options.project = buildProject
				}
				loadUsingRegistry := build.LoadUsingRegistry()
				if options.exportLoad && loadUsingRegistry {
					options.save = true
					pullInfo, err := depotbuild.PullBuildInfo(context.Background(), build.ID, token)
					// if we cannot get pull info, dont fail; load as normal
					if err == nil {
						options.loadUsingRegistry = loadUsingRegistry
						options.pullInfo = pullInfo
					}
				}
				if options.save {
					options.additionalCredentials = build.AdditionalCredentials()
					options.additionalTags = build.AdditionalTags()
				}
				options.buildID = build.ID
				options.buildURL = build.BuildURL
				options.token = build.Token
				options.build = &build

				if options.allowNoOutput {
					_ = os.Setenv("BUILDX_NO_DEFAULT_LOAD", "1")
				}

				func(c command.Cli, o BakeOptions, v BakeValidator, p *progresshelper.SharedPrinter) {
					eg.Go(func() error {
						var linter *Linter
						var requestedTargets []string

						buildErr := retryRetryableErrors(ctx, func() error {
							var err error
							linter, requestedTargets, err = RunBake(c, o, v, p)
							return err
						})
						if buildErr != nil {
							buildErr = rewriteFriendlyErrors(buildErr)
						}

						o.build.Finish(buildErr)
						PrintBuildURL(o.buildURL, o.progress)

						// Collect results for post-processing
						if buildErr == nil {
							result := buildResult{
								linter:           linter,
								requestedTargets: requestedTargets,
							}
							if o.save {
								result.saveInfo = &saveInfo{
									project:        o.project,
									buildID:        o.buildID,
									additionalTags: o.additionalTags,
								}
							}
							mu.Lock()
							buildResults = append(buildResults, result)
							mu.Unlock()
						}

						return buildErr
					})
				}(dockerCli, options, validator, printer)
			}

			// Wait for all builds to complete
			err = eg.Wait()

			// Now wait for the printer to finish and flush all output
			for range projectIDs {
				_ = printer.Wait()
			}

			// Print save help and linter output after all project builds complete
			for _, result := range buildResults {
				if result.saveInfo != nil {
					printSaveHelp(result.saveInfo.project, result.saveInfo.buildID,
						options.progress, result.requestedTargets, result.saveInfo.additionalTags)
				}
				if result.linter != nil {
					result.linter.Print(os.Stderr, options.progress)
				}
			}

			return err
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
	depotRegistryFlags(cmd, &options.DepotOptions, flags)

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

// BakeValidator returns either local or remote build options for targets as well as the targets themselves.
type BakeValidator interface {
	Validate(ctx context.Context, nodes []builder.Node, pw progress.Writer) (opts *bake.DepotBakeOptions, targets []string, err error)
}

type LocalBakeValidator struct {
	options     BakeOptions
	bakeTargets bakeTargets

	once      sync.Once
	buildOpts *bake.DepotBakeOptions
	targets   []string
	err       error
}

func NewLocalBakeValidator(options BakeOptions, args []string) *LocalBakeValidator {
	return &LocalBakeValidator{
		options:     options,
		bakeTargets: parseBakeTargets(args),
	}
}

func (t *LocalBakeValidator) Validate(ctx context.Context, _ []builder.Node, _ progress.Writer) (*bake.DepotBakeOptions, []string, error) {
	// Using a sync.Once because I _think_ the bake file may not always be read
	// more than one time such as passed over stdin.
	t.once.Do(func() {
		files, err := bake.ReadLocalFiles(t.options.files, os.Stdin)
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

		// Parse config to properly resolve groups
		c, err := bake.ParseFiles(files, defaults)
		if err != nil {
			t.err = err
			return
		}

		resolvedTargets := map[string]struct{}{}
		for _, target := range t.bakeTargets.Targets {
			// Use ResolveGroup to recursively resolve groups to their targets
			ts, _ := c.ResolveGroup(target)
			for _, tname := range ts {
				if _, ok := targets[tname]; ok {
					resolvedTargets[tname] = struct{}{}
				}
			}
		}
		for target := range resolvedTargets {
			t.targets = append(t.targets, target)
		}

		tags, err := compose.TargetTags(files)
		if err != nil {
			t.err = err
			return
		}

		for target, opts := range targets {
			if tag, ok := tags[target]; ok && len(opts.Tags) == 0 {
				opts.Tags = tag
			}
		}

		t.buildOpts, t.err = bake.NewDepotBakeOptions(t.options.project, targets, nil)
	})

	return t.buildOpts, t.targets, t.err
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

func (t *RemoteBakeValidator) Validate(ctx context.Context, nodes []builder.Node, pw progress.Writer) (*bake.DepotBakeOptions, []string, error) {
	files, inp, err := bake.ReadRemoteFiles(ctx, builder.ToBuildxNodes(nodes), t.bakeTargets.FileURL, t.options.files, pw)
	if err != nil {
		return nil, nil, err
	}

	overrides := overrides(t.options)
	defaults := map[string]string{
		"BAKE_CMD_CONTEXT":    t.bakeTargets.CmdContext,
		"BAKE_LOCAL_PLATFORM": platforms.DefaultString(),
	}

	targets, _, err := bake.ReadTargets(ctx, files, t.bakeTargets.Targets, overrides, defaults)
	if err != nil {
		return nil, nil, err
	}

	// Parse config to properly resolve groups
	c, err := bake.ParseFiles(files, defaults)
	if err != nil {
		return nil, nil, err
	}

	requestedTargets := []string{}
	uniqueTargets := map[string]struct{}{}
	for _, target := range t.bakeTargets.Targets {
		// Use ResolveGroup to recursively resolve groups to their targets
		ts, _ := c.ResolveGroup(target)
		for _, tname := range ts {
			if _, ok := targets[tname]; ok {
				uniqueTargets[tname] = struct{}{}
			}
		}
	}
	for target := range uniqueTargets {
		requestedTargets = append(requestedTargets, target)
	}

	opts, err := bake.NewDepotBakeOptions(t.options.project, targets, inp)
	return opts, requestedTargets, err
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

// printSaveHelp prints instructions to pull or push the saved targets.
func printSaveHelp(project, buildID, progressMode string, requestedTargets, additionalTags []string) {
	if progressMode != progress.PrinterModeQuiet && os.Getenv("DEPOT_NO_SUMMARY_LINK") == "" {
		fmt.Fprintln(os.Stderr)
		saved := "target"
		if len(requestedTargets) > 1 {
			saved += "s"
		}

		targetUsage := "--target <TARGET> "
		if len(requestedTargets) == 0 {
			targetUsage = ""
		}

		targets := strings.Join(requestedTargets, ",")
		fmt.Fprintf(os.Stderr, "Saved %s: %s\n", saved, targets)
		fmt.Fprintf(os.Stderr, "\tTo pull: depot pull --project %s %s\n", project, buildID)

		if len(additionalTags) > 1 {
			fmt.Fprintf(os.Stderr, "\tTo pull save-tags:\n")
			fmt.Fprintf(os.Stderr, "\t\tdocker login registry.depot.dev -u x-token -p $(depot pull-token)\n")
			fmt.Fprintln(os.Stderr)

			// the api will send multiple of the same tag back for each target
			if len(requestedTargets) > 0 {
				seenTags := map[string]struct{}{}
				for _, target := range requestedTargets {
					if target == "default" {
						continue
					}

					for _, tag := range additionalTags {
						if strings.Contains(tag, buildID) {
							continue
						}

						trueTag := tag + "-" + target
						if _, ok := seenTags[trueTag]; ok {
							continue
						}
						seenTags[trueTag] = struct{}{}

						fmt.Fprintf(os.Stderr, "\t\tdocker pull %s\n", trueTag)
					}
				}
			} else {
				for _, tag := range additionalTags {
					if strings.Contains(tag, buildID) {
						continue
					}

					fmt.Fprintf(os.Stderr, "\t\tdocker pull %s\n", tag)
				}
			}

			fmt.Fprintln(os.Stderr)
		}

		fmt.Fprintf(os.Stderr, "\tTo push: depot push %s--project %s --tag <REPOSITORY:TAG> %s\n", targetUsage, project, buildID)
	}
}
