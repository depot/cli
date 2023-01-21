package init

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bufbuild/connect-go"
	"github.com/containerd/containerd/platforms"
	depotapi "github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/project"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	"github.com/docker/buildx/bake"
	"github.com/docker/buildx/build"
	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/store/storeutil"
	"github.com/docker/buildx/util/confutil"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/buildx/util/tracing"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/context/docker"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/ioutils"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
)

type bakeOptions struct {
	project       string
	token         string
	allowNoOutput bool

	files     []string
	overrides []string
	printOnly bool

	//Common options
	builder      string
	metadataFile string
	noCache      *bool
	progress     string
	pull         *bool

	exportPush bool
	exportLoad bool
}

func dockerAPI(dockerCli command.Cli) *api {
	return &api{dockerCli: dockerCli}
}

type api struct {
	dockerCli command.Cli
}

func (a *api) DockerAPI(name string) (dockerclient.APIClient, error) {
	if name == "" {
		name = a.dockerCli.CurrentContext()
	}
	return clientForEndpoint(a.dockerCli, name)
}

// clientForEndpoint returns a docker client for an endpoint
func clientForEndpoint(dockerCli command.Cli, name string) (dockerclient.APIClient, error) {
	list, err := dockerCli.ContextStore().List()
	if err != nil {
		return nil, err
	}
	for _, l := range list {
		if l.Name == name {
			dep, ok := l.Endpoints["docker"]
			if !ok {
				return nil, errors.Errorf("context %q does not have a Docker endpoint", name)
			}
			epm, ok := dep.(docker.EndpointMeta)
			if !ok {
				return nil, errors.Errorf("endpoint %q is not of type EndpointMeta, %T", dep, dep)
			}
			ep, err := docker.WithTLSData(dockerCli.ContextStore(), name, epm)
			if err != nil {
				return nil, err
			}
			clientOpts, err := ep.ClientOpts()
			if err != nil {
				return nil, err
			}
			return dockerclient.NewClientWithOpts(clientOpts...)
		}
	}

	ep := docker.Endpoint{
		EndpointMeta: docker.EndpointMeta{
			Host: name,
		},
	}

	clientOpts, err := ep.ClientOpts()
	if err != nil {
		return nil, err
	}

	return dockerclient.NewClientWithOpts(clientOpts...)
}

func wrapBuildError(err error, bake bool) error {
	if err == nil {
		return nil
	}
	st, ok := grpcerrors.AsGRPCStatus(err)
	if ok {
		if st.Code() == codes.Unimplemented && strings.Contains(st.Message(), "unsupported frontend capability moby.buildkit.frontend.contexts") {
			msg := "current frontend does not support --build-context."
			if bake {
				msg = "current frontend does not support defining additional contexts for targets."
			}
			msg += " Named contexts are supported since Dockerfile v1.4. Use #syntax directive in Dockerfile or update to latest BuildKit."
			return &wrapped{err, msg}
		}
	}
	return err
}

func writeMetadataFile(filename string, dt interface{}) error {
	b, err := json.MarshalIndent(dt, "", "  ")
	if err != nil {
		return err
	}
	return ioutils.AtomicWriteFile(filename, b, 0644)
}

func decodeExporterResponse(exporterResponse map[string]string) map[string]interface{} {
	out := make(map[string]interface{})
	for k, v := range exporterResponse {
		dt, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			out[k] = v
			continue
		}
		var raw map[string]interface{}
		if err = json.Unmarshal(dt, &raw); err != nil || len(raw) == 0 {
			out[k] = v
			continue
		}
		out[k] = json.RawMessage(dt)
	}
	return out
}

type wrapped struct {
	err error
	msg string
}

func (w *wrapped) Error() string {
	return w.msg
}

func (w *wrapped) Unwrap() error {
	return w.err
}

func runBake(dockerCli command.Cli, targets []string, in bakeOptions) (err error) {
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
	req := cliv1beta1.CreateBuildRequest{ProjectId: in.project}
	b, err := client.CreateBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), in.token))
	if err != nil {
		return err
	}
	defer func() {
		req := cliv1beta1.FinishBuildRequest{BuildId: b.Msg.BuildId}
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

	dis, err := getDrivers(ctx, dockerCli, contextPathHash, b.Msg.BuildId, in.token)
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

	resp, err := build.Build(ctx, dis, bo, dockerAPI(dockerCli), confutil.ConfigDir(dockerCli), printer)
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

	return err
}

func getDrivers(ctx context.Context, dockerCli command.Cli, contextPathHash string, buildID string, token string) ([]build.DriverInfo, error) {
	imageopt, err := storeutil.GetImageConfig(dockerCli, nil)
	if err != nil {
		return nil, err
	}

	driverOpts := map[string]string{"token": token, "platform": "amd64", "buildID": buildID}
	amdDriver, err := driver.GetDriver(ctx, "buildx_buildkit_depot_amd64", nil, "", dockerCli.Client(), imageopt.Auth, nil, nil, nil, driverOpts, nil, contextPathHash)
	if err != nil {
		return nil, err
	}
	amdDriverInfo := build.DriverInfo{
		Name:     "depot",
		Driver:   amdDriver,
		ImageOpt: imageopt,
		Platform: []v1.Platform{
			{OS: "linux", Architecture: "amd64"},
			{OS: "linux", Architecture: "amd64", Variant: "v2"},
			{OS: "linux", Architecture: "amd64", Variant: "v3"},
			{OS: "linux", Architecture: "386"},
		},
	}

	driverOpts = map[string]string{"token": token, "platform": "arm64", "buildID": buildID}
	armDriver, err := driver.GetDriver(ctx, "buildx_buildkit_depot_arm64", nil, "", dockerCli.Client(), imageopt.Auth, nil, nil, nil, driverOpts, nil, contextPathHash)
	if err != nil {
		return nil, err
	}
	armDriverInfo := build.DriverInfo{
		Name:     "depot",
		Driver:   armDriver,
		ImageOpt: imageopt,
		Platform: []v1.Platform{
			{OS: "linux", Architecture: "arm64"},
			{OS: "linux", Architecture: "arm", Variant: "v7"},
			{OS: "linux", Architecture: "arm", Variant: "v6"},
		},
	}

	if strings.HasPrefix(runtime.GOARCH, "arm") {
		return []build.DriverInfo{armDriverInfo, amdDriverInfo}, nil
	}
	return []build.DriverInfo{amdDriverInfo, armDriverInfo}, nil
}

func NewCmdCache() *cobra.Command {
	options := bakeOptions{}

	cmd := &cobra.Command{
		Use:   "bake [OPTIONS] [TARGET...]",
		Short: "Run a build from a file on Depot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if options.project == "" {
				options.project = os.Getenv("DEPOT_PROJECT_ID")
			}
			if options.project == "" {
				cwd, _ := filepath.Abs(args[0])
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

			dockerCli, err := utils.NewDockerCLI()
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
