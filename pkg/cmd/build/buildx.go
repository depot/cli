package build

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	depotapi "github.com/depot/cli/pkg/api"
	"github.com/docker/buildx/build"
	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/store/storeutil"
	"github.com/docker/buildx/util/buildflags"
	"github.com/docker/buildx/util/confutil"
	"github.com/docker/buildx/util/platformutil"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/buildx/util/tracing"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/context/docker"
	dockeropts "github.com/docker/cli/opts"
	"github.com/docker/distribution/reference"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/go-units"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/morikuni/aec"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
)

const defaultTargetName = "default"

type buildOptions struct {
	project string
	token   string

	contextPath    string
	dockerfileName string

	allow         []string
	buildArgs     []string
	cacheFrom     []string
	cacheTo       []string
	cgroupParent  string
	contexts      []string
	extraHosts    []string
	imageIDFile   string
	labels        []string
	networkMode   string
	noCacheFilter []string
	outputs       []string
	platforms     []string
	quiet         bool
	secrets       []string
	shmSize       dockeropts.MemBytes
	ssh           []string
	tags          []string
	target        string
	ulimits       *dockeropts.UlimitOpt

	// common options
	metadataFile string
	noCache      *bool
	progress     string
	pull         *bool

	// golangci-lint#826
	// nolint:structcheck
	exportPush bool
	// nolint:structcheck
	exportLoad bool

	noLoad bool
}

func runBuild(dockerCli command.Cli, in buildOptions) (err error) {
	ctx := appcontext.Context()

	ctx, end, err := tracing.TraceCurrentCommand(ctx, "build")
	if err != nil {
		return err
	}
	defer func() {
		end(err)
	}()

	noCache := false
	if in.noCache != nil {
		noCache = *in.noCache
	}
	pull := false
	if in.pull != nil {
		pull = *in.pull
	}

	if noCache && len(in.noCacheFilter) > 0 {
		return errors.Errorf("--no-cache and --no-cache-filter cannot currently be used together")
	}

	if in.quiet && in.progress != "auto" && in.progress != "quiet" {
		return errors.Errorf("progress=%s and quiet cannot be used together", in.progress)
	} else if in.quiet {
		in.progress = "quiet"
	}

	contexts, err := parseContextNames(in.contexts)
	if err != nil {
		return err
	}

	opts := build.Options{
		Inputs: build.Inputs{
			ContextPath:    in.contextPath,
			DockerfilePath: in.dockerfileName,
			InStream:       os.Stdin,
			NamedContexts:  contexts,
		},
		BuildArgs:     listToMap(in.buildArgs, true),
		ExtraHosts:    in.extraHosts,
		ImageIDFile:   in.imageIDFile,
		Labels:        listToMap(in.labels, false),
		NetworkMode:   in.networkMode,
		NoCache:       noCache,
		NoCacheFilter: in.noCacheFilter,
		Pull:          pull,
		ShmSize:       in.shmSize,
		Tags:          in.tags,
		Target:        in.target,
		Ulimits:       in.ulimits,
	}

	platforms, err := platformutil.Parse(in.platforms)
	if err != nil {
		return err
	}
	opts.Platforms = platforms

	opts.Session = append(opts.Session, authprovider.NewDockerAuthProvider(os.Stderr))

	secrets, err := buildflags.ParseSecretSpecs(in.secrets)
	if err != nil {
		return err
	}
	opts.Session = append(opts.Session, secrets)

	sshSpecs := in.ssh
	if len(sshSpecs) == 0 && buildflags.IsGitSSH(in.contextPath) {
		sshSpecs = []string{"default"}
	}
	ssh, err := buildflags.ParseSSHSpecs(sshSpecs)
	if err != nil {
		return err
	}
	opts.Session = append(opts.Session, ssh)

	outputs, err := buildflags.ParseOutputs(in.outputs)
	if err != nil {
		return err
	}
	if in.exportPush {
		if in.exportLoad {
			return errors.Errorf("push and load may not be set together at the moment")
		}
		if len(outputs) == 0 {
			outputs = []client.ExportEntry{{
				Type: "image",
				Attrs: map[string]string{
					"push": "true",
				},
			}}
		} else {
			switch outputs[0].Type {
			case "image":
				outputs[0].Attrs["push"] = "true"
			default:
				return errors.Errorf("push and %q output can't be used together", outputs[0].Type)
			}
		}
	}
	if in.exportLoad {
		if len(outputs) == 0 {
			outputs = []client.ExportEntry{{
				Type:  "docker",
				Attrs: map[string]string{},
			}}
		} else {
			switch outputs[0].Type {
			case "docker":
			default:
				return errors.Errorf("load and %q output can't be used together", outputs[0].Type)
			}
		}
	}

	opts.Exports = outputs

	cacheImports, err := buildflags.ParseCacheEntry(in.cacheFrom)
	if err != nil {
		return err
	}
	opts.CacheFrom = cacheImports

	cacheExports, err := buildflags.ParseCacheEntry(in.cacheTo)
	if err != nil {
		return err
	}
	opts.CacheTo = cacheExports

	allow, err := buildflags.ParseEntitlements(in.allow)
	if err != nil {
		return err
	}
	opts.Allow = allow

	// key string used for kubernetes "sticky" mode
	contextPathHash, err := filepath.Abs(in.contextPath)
	if err != nil {
		contextPathHash = in.contextPath
	}

	imageID, err := buildTargets(ctx, dockerCli, map[string]build.Options{defaultTargetName: opts}, in.progress, contextPathHash, in.metadataFile, in)
	err = wrapBuildError(err, false)
	if err != nil {
		return err
	}

	if in.quiet {
		fmt.Println(imageID)
	}
	return nil
}

func buildTargets(ctx context.Context, dockerCli command.Cli, opts map[string]build.Options, progressMode, contextPathHash, metadataFile string, in buildOptions) (imageID string, err error) {
	ctx2, cancel := context.WithCancel(context.TODO())
	defer cancel()

	printer := progress.NewPrinter(ctx2, os.Stderr, os.Stderr, progressMode)

	depot, err := depotapi.NewDepotFromEnv(in.token)
	if err != nil {
		return "", err
	}
	ctx = depotapi.WithClient(ctx, depot)

	b, err := depot.CreateBuild(in.project)
	if err != nil {
		return "", err
	}
	defer func() {
		err := depot.FinishBuild(b.ID)
		if err != nil {
			log.Printf("error releasing builder: %v", err)
		}
	}()

	dis, err := getDrivers(ctx, dockerCli, contextPathHash, b.ID)
	if err != nil {
		return "", err
	}

	resp, err := build.Build(ctx, dis, opts, dockerAPI(dockerCli), confutil.ConfigDir(dockerCli), printer)
	err1 := printer.Wait()
	if err == nil {
		err = err1
	}
	if err != nil {
		return "", err
	}

	if len(metadataFile) > 0 && resp != nil {
		if err := writeMetadataFile(metadataFile, decodeExporterResponse(resp[defaultTargetName].ExporterResponse)); err != nil {
			return "", err
		}
	}

	printWarnings(os.Stderr, printer.Warnings(), progressMode)

	return resp[defaultTargetName].ExporterResponse["containerimage.digest"], err
}

func getDrivers(ctx context.Context, dockerCli command.Cli, contextPathHash string, buildID string) ([]build.DriverInfo, error) {
	imageopt, err := storeutil.GetImageConfig(dockerCli, nil)
	if err != nil {
		return nil, err
	}

	driverOpts := map[string]string{"platform": "amd64", "buildID": buildID}
	amdDriver, err := driver.GetDriver(ctx, "buildx_buildkit_depot_amd64", nil, dockerCli.Client(), imageopt.Auth, nil, nil, nil, driverOpts, nil, contextPathHash)
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

	driverOpts = map[string]string{"platform": "arm64", "buildID": buildID}
	armDriver, err := driver.GetDriver(ctx, "buildx_buildkit_depot_arm64", nil, dockerCli.Client(), imageopt.Auth, nil, nil, nil, driverOpts, nil, contextPathHash)
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

func newBuildOptions() buildOptions {
	ulimits := make(map[string]*units.Ulimit)
	return buildOptions{
		ulimits: dockeropts.NewUlimitOpt(&ulimits),
	}
}

func listToMap(values []string, defaultEnv bool) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		kv := strings.SplitN(value, "=", 2)
		if len(kv) == 1 {
			if defaultEnv {
				v, ok := os.LookupEnv(kv[0])
				if ok {
					result[kv[0]] = v
				}
			} else {
				result[kv[0]] = ""
			}
		} else {
			result[kv[0]] = kv[1]
		}
	}
	return result
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

func printWarnings(w io.Writer, warnings []client.VertexWarning, mode string) {
	if len(warnings) == 0 || mode == progress.PrinterModeQuiet {
		return
	}
	fmt.Fprintf(w, "\n ")
	sb := &bytes.Buffer{}
	if len(warnings) == 1 {
		fmt.Fprintf(sb, "1 warning found")
	} else {
		fmt.Fprintf(sb, "%d warnings found", len(warnings))
	}
	if logrus.GetLevel() < logrus.DebugLevel {
		fmt.Fprintf(sb, " (use --debug to expand)")
	}
	fmt.Fprintf(sb, ":\n")
	fmt.Fprint(w, aec.Apply(sb.String(), aec.YellowF))

	for _, warn := range warnings {
		fmt.Fprintf(w, " - %s\n", warn.Short)
		if logrus.GetLevel() < logrus.DebugLevel {
			continue
		}
		for _, d := range warn.Detail {
			fmt.Fprintf(w, "%s\n", d)
		}
		if warn.URL != "" {
			fmt.Fprintf(w, "More info: %s\n", warn.URL)
		}
		if warn.SourceInfo != nil && warn.Range != nil {
			src := errdefs.Source{
				Info:   warn.SourceInfo,
				Ranges: warn.Range,
			}
			src.Print(w)
		}
		fmt.Fprintf(w, "\n")

	}
}

func parseContextNames(values []string) (map[string]build.NamedContext, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make(map[string]build.NamedContext, len(values))
	for _, value := range values {
		kv := strings.SplitN(value, "=", 2)
		if len(kv) != 2 {
			return nil, errors.Errorf("invalid context value: %s, expected key=value", value)
		}
		named, err := reference.ParseNormalizedNamed(kv[0])
		if err != nil {
			return nil, errors.Wrapf(err, "invalid context name %s", kv[0])
		}
		name := strings.TrimSuffix(reference.FamiliarString(named), ":latest")
		result[name] = build.NamedContext{Path: kv[1]}
	}
	return result, nil
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
