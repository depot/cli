// Source: https://github.com/docker/buildx/blob/v0.10/commands/bake.go

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/containerd/containerd/platforms"
	"github.com/depot/cli/pkg/buildx/builder"
	"github.com/depot/cli/pkg/helpers"
	"github.com/docker/buildx/bake"
	"github.com/docker/buildx/build"
	"github.com/docker/buildx/util/buildflags"
	"github.com/docker/buildx/util/confutil"
	"github.com/docker/buildx/util/dockerutil"
	"github.com/docker/buildx/util/tracing"
	"github.com/docker/cli/cli/command"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
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

	ctx2, cancel := context.WithCancel(ctx)

	printer, err := NewProgress(ctx2, in.buildID, in.token, in.progress)
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
	bo, err := bake.TargetsToBuildOpt(tgts, inp)
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

	toPull := []PullOptions{}
	if in.exportLoad {
		// Push to the depot user's personal registry to allow us to pull layers in parallel.
		for _, buildOpt := range bo {
			// TODO: figureout the best depotImageName.  Something from the builtOpt?
			depotImageName := fmt.Sprintf("ecr.io/your-registry/your-image:%s", in.buildID)

			var shouldPull bool
			if len(buildOpt.Exports) == 0 {
				shouldPull = true
				buildOpt.Exports = []client.ExportEntry{
					{Type: "image", Attrs: map[string]string{"name": depotImageName, "push": "true"}}}
			} else {
				for _, export := range buildOpt.Exports {
					// Only pull if the user asked for an import export.
					if export.Type == "image" {
						shouldPull = true
						if name, ok := export.Attrs["name"]; ok {
							// Also, push to user's private depot registry as well as the original registry.
							export.Attrs["name"] = fmt.Sprintf("%s,%s", name, depotImageName)
							export.Attrs["push"] = "true"
						} else {
							export.Attrs["name"] = depotImageName
							export.Attrs["push"] = "true"
						}
					}
				}
			}

			if shouldPull {
				pullOpt := PullOptions{
					UserTag:            buildOpt.Tags[0], // TODO: not sure about this.  no tag? and is this the image name?
					DepotTag:           depotImageName,
					DepotRegistryURL:   "https://ecr.io", // TODO:
					DepotRegistryToken: in.token,
					Quiet:              false, // TODO: does bake have a quiet option?
				}
				toPull = append(toPull, pullOpt)
			}
		}
	}

	resp, err := build.Build(ctx, builder.ToBuildxNodes(nodes), bo, dockerutil.NewClient(dockerCli), confutil.ConfigDir(dockerCli), printer)
	if err != nil {
		return wrapBuildError(err, true)
	}

	if len(in.metadataFile) > 0 {
		dt := make(map[string]interface{})
		for t, r := range resp {
			dt[t] = decodeExporterResponse(r.ExporterResponse)
		}
		if err := writeMetadataFile(in.metadataFile, dt); err != nil {
			return err
		}
	}

	for _, pullOpt := range toPull {
		if err := PullImages(ctx, dockerCli.Client(), pullOpt, printer); err != nil {
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

			token := helpers.ResolveToken(options.token)
			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			project := helpers.ResolveProjectID(options.project, cwd)
			if project == "" {
				return errors.Errorf("unknown project ID (run `depot init` or use --project or $DEPOT_PROJECT_ID)")
			}
			buildPlatform, err := helpers.ResolveBuildPlatform(options.buildPlatform)
			if err != nil {
				return err
			}

			buildID, finishBuild, err := helpers.BeginBuild(context.Background(), project, token)
			if err != nil {
				return err
			}
			var buildErr error
			defer func() {
				finishBuild(buildErr)
			}()
			options.builderOptions = []builder.Option{builder.WithDepotOptions(token, buildID, buildPlatform)}
			options.buildID = buildID
			options.token = token

			if options.allowNoOutput {
				_ = os.Setenv("BUILDX_NO_DEFAULT_LOAD", "1")
			}

			buildErr = RunBake(dockerCli, args, options)
			return buildErr
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
