package init

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/bufbuild/connect-go"
	"github.com/containerd/containerd/platforms"
	depotapi "github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/buildx"
	"github.com/depot/cli/pkg/config"
	dockerclient "github.com/depot/cli/pkg/docker"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/project"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	"github.com/depot/cli/pkg/traces"
	"github.com/docker/buildx/bake"
	"github.com/docker/buildx/build"
	"github.com/docker/buildx/util/confutil"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/cli/cli/command"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type bakeOptions struct {
	project       string
	token         string
	buildPlatform string

	files     []string
	overrides []string
	printOnly bool

	//Common options
	metadataFile string
	noCache      *bool
	progress     string
	pull         *bool

	exportPush bool
	exportLoad bool
}

func runBake(dockerCli command.Cli, targets []string, in bakeOptions) (err error) {
	ctx := appcontext.Context()

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
		if in.exportLoad {
			return errors.Errorf("push and load may not be set together at the moment")
		}
		overrides = append(overrides, "*.push=true")
	} else if in.exportLoad {
		overrides = append(overrides, "*.output=type=docker")
	}
	if in.noCache != nil {
		overrides = append(overrides, fmt.Sprintf("*.no-cache=%t", *in.noCache))
	}
	if in.pull != nil {
		overrides = append(overrides, fmt.Sprintf("*.pull=%t", *in.pull))
	}
	contextPathHash, _ := os.Getwd()

	ctx2, cancel := context.WithCancel(context.TODO())
	defer cancel()
	printer := progress.NewPrinter(ctx2, os.Stderr, os.Stderr, in.progress)

	defer func() {
		if printer != nil {
			err1 := printer.Wait()
			if err == nil {
				err = err1
			}
		}
	}()

	client := depotapi.NewBuildClient()

	var buildErr error

	buildID := os.Getenv("DEPOT_BUILD_ID")
	if buildID == "" {
		req := cliv1beta1.CreateBuildRequest{ProjectId: in.project}
		b, err := client.CreateBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), in.token))
		if err != nil {
			return err
		}
		buildID = b.Msg.BuildId
	}

	ctx, end, err := traces.TraceCommand(ctx, "bake", buildID, in.token)
	if err != nil {
		return err
	}
	defer func() {
		end(err)
	}()

	defer func() {
		req := cliv1beta1.FinishBuildRequest{BuildId: buildID}
		req.Result = &cliv1beta1.FinishBuildRequest_Success{Success: &cliv1beta1.FinishBuildRequest_BuildSuccess{}}
		if buildErr != nil {
			errorMessage := ""
			if depotapi.IsDepotError(buildErr) {
				errorMessage = buildErr.Error()
			}
			req.Result = &cliv1beta1.FinishBuildRequest_Error{Error: &cliv1beta1.FinishBuildRequest_BuildError{Error: errorMessage}}
		}
		_, err := client.FinishBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), in.token))
		if err != nil {
			log.Printf("error releasing builder: %v", err)
		}
	}()

	dis, err := buildx.GetDrivers(ctx, dockerCli, contextPathHash, buildID, in.token, in.buildPlatform)
	if err != nil {
		return err
	}

	var files []bake.File
	var inp *bake.Input

	if url != "" {
		files, inp, err = bake.ReadRemoteFiles(ctx, dis, url, in.files, printer)
	} else {
		files, err = bake.ReadLocalFiles(in.files)
	}
	if err != nil {
		return err
	}

	tgts, grps, err := bake.ReadTargets(ctx, files, targets, overrides, map[string]string{
		// don't forget to update documentation if you add a new
		// built-in variable: docs/guides/bake/file-definition.md#built-in-variables
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
		var defg map[string]*bake.Group
		if len(grps) == 1 {
			defg = map[string]*bake.Group{
				"default": grps[0],
			}
		}
		dt, err := json.MarshalIndent(struct {
			Group  map[string]*bake.Group  `json:"group,omitempty"`
			Target map[string]*bake.Target `json:"target"`
		}{
			defg,
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

	resp, err := build.Build(ctx, dis, bo, buildx.DockerAPI(dockerCli), confutil.ConfigDir(dockerCli), printer)
	if err != nil {
		return buildx.WrapBuildError(err, true)
	}

	if len(in.metadataFile) > 0 {
		dt := make(map[string]interface{})
		for t, r := range resp {
			dt[t] = buildx.DecodeExporterResponse(r.ExporterResponse)
		}
		if err := buildx.WriteMetadataFile(in.metadataFile, dt); err != nil {
			return err
		}
	}

	return err
}

func NewCmdBake() *cobra.Command {
	options := bakeOptions{}

	cmd := &cobra.Command{
		Use:   "bake [OPTIONS] [TARGET...]",
		Short: "Run a build from a file on Depot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if options.project == "" {
				options.project = os.Getenv("DEPOT_PROJECT_ID")
			}
			if options.project == "" {
				cwd, _ := os.Getwd()
				config, _, err := project.ReadConfig(cwd)
				if err == nil {
					options.project = config.ID
				}
			}
			if options.project == "" {
				return errors.Errorf("unknown project ID (run `depot init` or use --project or $DEPOT_PROJECT_ID)")
			}

			// TODO: make this a helper
			if options.token == "" {
				options.token = os.Getenv("DEPOT_TOKEN")
			}
			if options.token == "" {
				options.token = config.GetApiToken()
			}
			if options.token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			buildPlatform, err := helpers.NormalizePlatform(options.buildPlatform)
			if err != nil {
				return err
			}
			options.buildPlatform = buildPlatform

			dockerCli, err := dockerclient.NewDockerCLI()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}

			return runBake(dockerCli, args, options)
		},
	}

	flags := cmd.Flags()

	// Depot options
	flags.StringVar(&options.project, "project", "", "Depot project ID")
	flags.StringVar(&options.token, "token", "", "Depot API token")
	flags.StringVar(&options.buildPlatform, "build-platform", "dynamic", `Run builds on this platform ("dynamic", "linux/amd64", "linux/arm64")`)

	// docker buildx bake options
	flags.StringArrayVarP(&options.files, "file", "f", []string{}, "Build definition file")
	flags.BoolVar(&options.exportLoad, "load", false, `Shorthand for "--set=*.output=type=docker"`)
	flags.BoolVar(&options.printOnly, "print", false, "Print the options without building")
	flags.BoolVar(&options.exportPush, "push", false, `Shorthand for "--set=*.output=type=registry"`)
	flags.StringArrayVar(&options.overrides, "set", nil, `Override target value (e.g., "targetpattern.key=value")`)

	// Common options
	options.noCache = flags.Bool("no-cache", false, "Do not use cache when building the image")
	flags.StringVar(&options.progress, "progress", "auto", `Set type of progress output ("auto", "plain", "tty"). Use plain to show container output`)
	options.pull = flags.Bool("pull", false, "Always attempt to pull all referenced images")
	flags.StringVar(&options.metadataFile, "metadata-file", "", "Write build result metadata to the file")

	return cmd
}
