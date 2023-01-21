package buildx

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/bufbuild/connect-go"
	depotapi "github.com/depot/cli/pkg/api"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	"github.com/docker/buildx/build"
	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/store/storeutil"
	"github.com/docker/buildx/util/confutil"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/context/docker"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/api/types/versions"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/subrequests"
	"github.com/moby/buildkit/frontend/subrequests/outline"
	"github.com/moby/buildkit/frontend/subrequests/targets"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/morikuni/aec"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
)

const DefaultTargetName = "default"

func BuildTargets(ctx context.Context, dockerCli command.Cli, opts map[string]build.Options, progressMode, contextPathHash, metadataFile string, project string, token string, allowNoOutput bool) (imageID string, err error) {
	var buildErr error

	ctx2, cancel := context.WithCancel(context.TODO())
	defer cancel()

	printer := progress.NewPrinter(ctx2, os.Stderr, os.Stderr, progressMode)

	client := depotapi.NewBuildClient()

	req := cliv1beta1.CreateBuildRequest{ProjectId: project}
	b, err := client.CreateBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), token))
	if err != nil {
		return "", err
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
		_, err := client.FinishBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), token))
		if err != nil {
			log.Printf("error releasing builder: %v", err)
		}
	}()

	dis, err := getDrivers(ctx, dockerCli, contextPathHash, b.Msg.BuildId, token)
	if err != nil {
		return "", err
	}

	resp, buildErr := build.BuildWithResultHandler(ctx, dis, opts, dockerAPI(dockerCli), confutil.ConfigDir(dockerCli), printer, nil, allowNoOutput)
	err1 := printer.Wait()
	if buildErr == nil {
		buildErr = err1
	}
	if buildErr != nil {
		return "", buildErr
	}

	if len(metadataFile) > 0 && resp != nil {
		if err := writeMetadataFile(metadataFile, decodeExporterResponse(resp[DefaultTargetName].ExporterResponse)); err != nil {
			return "", err
		}
	}

	printWarnings(os.Stderr, printer.Warnings(), progressMode)

	// Attempt to stop drivers, which will stop health checks
	for _, d := range dis {
		_ = d.Driver.Stop(ctx, false)
	}

	for k := range resp {
		if opts[k].PrintFunc != nil {
			if err := printResult(opts[k].PrintFunc, resp[k].ExporterResponse); err != nil {
				return "", err
			}
		}
	}

	return resp[DefaultTargetName].ExporterResponse["containerimage.digest"], err
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

func ListToMap(values []string, defaultEnv bool) map[string]string {
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

func printResult(f *build.PrintFunc, res map[string]string) error {
	switch f.Name {
	case "outline":
		return printValue(outline.PrintOutline, outline.SubrequestsOutlineDefinition.Version, f.Format, res)
	case "targets":
		return printValue(targets.PrintTargets, targets.SubrequestsTargetsDefinition.Version, f.Format, res)
	case "subrequests.describe":
		return printValue(subrequests.PrintDescribe, subrequests.SubrequestsDescribeDefinition.Version, f.Format, res)
	default:
		if dt, ok := res["result.txt"]; ok {
			fmt.Print(dt)
		} else {
			log.Printf("%s %+v", f, res)
		}
	}
	return nil
}

type printFunc func([]byte, io.Writer) error

func printValue(printer printFunc, version string, format string, res map[string]string) error {
	if format == "json" {
		fmt.Fprintln(os.Stdout, res["result.json"])
		return nil
	}

	if res["version"] != "" && versions.LessThan(version, res["version"]) && res["result.txt"] != "" {
		// structure is too new and we don't know how to print it
		fmt.Fprint(os.Stdout, res["result.txt"])
		return nil
	}
	return printer([]byte(res["result.json"]), os.Stdout)
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

func ParseContextNames(values []string) (map[string]build.NamedContext, error) {
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

func ParsePrintFunc(str string) (*build.PrintFunc, error) {
	if str == "" {
		return nil, nil
	}
	csvReader := csv.NewReader(strings.NewReader(str))
	fields, err := csvReader.Read()
	if err != nil {
		return nil, err
	}
	f := &build.PrintFunc{}
	for _, field := range fields {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) == 2 {
			if parts[0] == "format" {
				f.Format = parts[1]
			} else {
				return nil, errors.Errorf("invalid print field: %s", field)
			}
		} else {
			if f.Name != "" {
				return nil, errors.Errorf("invalid print value: %s", str)
			}
			f.Name = field
		}
	}
	return f, nil
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

func WrapBuildError(err error, bake bool) error {
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

func IsExperimental() bool {
	if v, ok := os.LookupEnv("BUILDX_EXPERIMENTAL"); ok {
		vv, _ := strconv.ParseBool(v)
		return vv
	}
	return false
}
