// Source: https://github.com/docker/buildx/blob/v0.10/commands/bake.go

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/containerd/containerd/platforms"
	"github.com/depot/cli/pkg/buildx/build"
	"github.com/depot/cli/pkg/buildx/builder"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/load"
	depotprogress "github.com/depot/cli/pkg/progress"
	"github.com/docker/buildx/bake"
	buildx "github.com/docker/buildx/build"
	"github.com/docker/buildx/util/buildflags"
	"github.com/docker/buildx/util/confutil"
	"github.com/docker/buildx/util/dockerutil"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/buildx/util/tracing"
	"github.com/docker/cli/cli/command"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
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

func RunBake(dockerCli command.Cli, targets []string, in BakeOptions) (err error) {
	ctx := appcontext.Context()

	ctx, end, err := tracing.TraceCurrentCommand(ctx, "bake")
	if err != nil {
		return err
	}
	defer func() {
		end(err)
	}()

	var url string
	cmdContext := "cwd://"

	if len(targets) > 0 {
		if bake.IsRemoteURL(targets[0]) {
			url = targets[0]
			targets = targets[1:]
			if len(targets) > 0 {
				if bake.IsRemoteURL(targets[0]) {
					cmdContext = targets[0]
					targets = targets[1:]
				}
			}
		}
	}

	if len(targets) == 0 {
		targets = []string{"default"}
	}

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
	contextPathHash, _ := os.Getwd()

	ctx2, cancel := context.WithCancel(context.TODO())

	printer, err := depotprogress.NewProgress(ctx2, in.buildID, in.token, in.progress)
	if err != nil {
		cancel()
		return err
	}

	defer func() {
		if printer != nil {
			err1 := printer.Wait()
			if err == nil && !errors.Is(err1, context.Canceled) {
				err = err1
			}
		}
	}()

	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		printer.Run(ctx2)
		wg.Done()
	}()
	defer wg.Wait() // Required to ensure that the printer is stopped before the context is cancelled.
	defer cancel()

	var nodes []builder.Node
	var files []bake.File
	var inp *bake.Input

	// instance only needed for reading remote bake files or building
	if url != "" || !in.printOnly {
		builderOpts := append([]builder.Option{builder.WithName(in.builder),
			builder.WithContextPathHash(contextPathHash)}, in.builderOptions...)
		b, err := builder.New(dockerCli, builderOpts...)
		if err != nil {
			return err
		}
		if err = updateLastActivity(dockerCli, b.NodeGroup); err != nil {
			return errors.Wrapf(err, "failed to update builder last activity time")
		}
		nodes, err = b.LoadNodes(ctx, false)
		if err != nil {
			return err
		}
	}

	if url != "" {
		files, inp, err = bake.ReadRemoteFiles(ctx, builder.ToBuildxNodes(nodes), url, in.files, printer)
	} else {
		files, err = bake.ReadLocalFiles(in.files)
	}
	if err != nil {
		return err
	}

	tgts, grps, err := bake.ReadTargets(ctx, files, targets, overrides, map[string]string{
		// don't forget to update documentation if you add a new
		// built-in variable: docs/manuals/bake/file-definition.md#built-in-variables
		"BAKE_CMD_CONTEXT":    cmdContext,
		"BAKE_LOCAL_PLATFORM": platforms.DefaultString(),
	})
	if err != nil {
		return err
	}

	// this function can update target context string from the input so call before printOnly check
	buildOpts, err := bake.TargetsToBuildOpt(tgts, inp)
	if err != nil {
		return err
	}

	if in.printOnly {
		dt, err := json.MarshalIndent(struct {
			Group  map[string]*bake.Group  `json:"group,omitempty"`
			Target map[string]*bake.Target `json:"target"`
		}{
			grps,
			tgts,
		}, "", "  ")
		if err != nil {
			return err
		}
		err = printer.Wait()
		printer = nil
		if err != nil {
			return err
		}
		fmt.Fprintln(dockerCli.Out(), string(dt))
		return nil
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
				ProxyImage:       in.DepotOptions.proxyImage,
				Project:          in.DepotOptions.project,
				BuildID:          in.DepotOptions.buildID,
				IsBake:           true,
				ProgressMode:     in.progress,
			},
		)
	}

	buildxNodes := builder.ToBuildxNodes(nodes)
	dockerClient := dockerutil.NewClient(dockerCli)
	dockerConfigDir := confutil.ConfigDir(dockerCli)

	resp, err := build.DepotBuild(ctx, buildxNodes, buildOpts, dockerClient, dockerConfigDir, printer)
	if err != nil {
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

				if len(nodeRes.ManifestConfigs) > 0 {
					metadata[exptypes.ExporterImageDescriptorKey] = nodeRes.ManifestConfigs[0].Desc
					metadata[exptypes.ExporterImageConfigKey] = nodeRes.ManifestConfigs[0].ImageConfig
					metadata["containerimage.manifest"] = nodeRes.ManifestConfigs[0].Manifest
				}
			}
			dt[buildRes.Name] = metadata
		}
		if err := writeMetadataFile(in.metadataFile, dt); err != nil {
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
				_, err = build.DepotBuild(ctx, buildxNodes, buildOpts, dockerClient, dockerConfigDir, printer)
			}

			return err
		}
	}

	return nil
}

func BakeCmd(dockerCli command.Cli) *cobra.Command {
	var options BakeOptions

	cmd := &cobra.Command{
		Use:     "bake [OPTIONS] [TARGET...]",
		Aliases: []string{"f"},
		Short:   "Build from a file",
		RunE: func(cmd *cobra.Command, args []string) error {
			// reset to nil to avoid override is unset
			if !cmd.Flags().Lookup("no-cache").Changed {
				options.noCache = nil
			}
			if !cmd.Flags().Lookup("pull").Changed {
				options.pull = nil
			}

			token := helpers.ResolveToken(context.Background(), options.token)
			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			options.project = helpers.ResolveProjectID(options.project, options.files...)
			if options.project == "" {
				return errors.Errorf("unknown project ID (run `depot init` or use --project or $DEPOT_PROJECT_ID)")
			}

			buildPlatform, err := helpers.ResolveBuildPlatform(options.buildPlatform)
			if err != nil {
				return err
			}

			build, err := helpers.BeginBuild(context.Background(), options.project, token)
			if err != nil {
				return err
			}
			var buildErr error
			defer func() {
				build.Finish(buildErr)
			}()

			options.builderOptions = []builder.Option{builder.WithDepotOptions(buildPlatform, build)}

			options.buildID = build.ID
			options.token = build.Token
			options.useLocalRegistry = build.UseLocalRegistry
			options.proxyImage = build.ProxyImage

			if options.allowNoOutput {
				_ = os.Setenv("BUILDX_NO_DEFAULT_LOAD", "1")
			}

			buildErr = retryRetryableErrors(context.Background(), func() error {
				return RunBake(dockerCli, args, options)
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
	depotBuildFlags(&options.DepotOptions, flags)

	return cmd
}
