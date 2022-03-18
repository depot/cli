package build

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	depotapi "github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/builder"
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
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/go-units"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/pkg/errors"
)

const defaultTargetName = "default"

type buildOptions struct {
	project string
	token   string

	contextPath    string
	dockerfileName string

	allow        []string
	buildArgs    []string
	cacheFrom    []string
	cacheTo      []string
	cgroupParent string
	extraHosts   []string
	imageIDFile  string
	labels       []string
	networkMode  string
	outputs      []string
	platforms    []string
	quiet        bool
	secrets      []string
	shmSize      dockeropts.MemBytes
	ssh          []string
	tags         []string
	target       string
	ulimits      *dockeropts.UlimitOpt
	commonOptions
}

type commonOptions struct {
	metadataFile string
	noCache      *bool
	progress     string
	pull         *bool

	// golangci-lint#826
	// nolint:structcheck
	exportPush bool
	// nolint:structcheck
	exportLoad bool
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

	if in.quiet && in.progress != "auto" && in.progress != "quiet" {
		return errors.Errorf("progress=%s and quiet cannot be used together", in.progress)
	} else if in.quiet {
		in.progress = "quiet"
	}

	opts := build.Options{
		Inputs: build.Inputs{
			ContextPath:    in.contextPath,
			DockerfilePath: in.dockerfileName,
			InStream:       os.Stdin,
		},
		BuildArgs:   listToMap(in.buildArgs, true),
		ExtraHosts:  in.extraHosts,
		ImageIDFile: in.imageIDFile,
		Labels:      listToMap(in.labels, false),
		NetworkMode: in.networkMode,
		NoCache:     noCache,
		Pull:        pull,
		ShmSize:     in.shmSize,
		Tags:        in.tags,
		Target:      in.target,
		Ulimits:     in.ulimits,
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

	printer := progress.NewPrinter(ctx2, os.Stderr, progressMode)

	depot, err := depotapi.NewDepotFromEnv(in.token)
	if err != nil {
		return "", err
	}

	b := builder.NewBuilder(depot)
	addr, err := b.Acquire(printer.Write, in.project)
	if err != nil {
		return "", err
	}
	defer func() {
		err := b.Release()
		if err != nil {
			log.Printf("error releasing builder: %v", err)
		}
	}()

	dis, err := getDrivers(ctx, dockerCli, contextPathHash, addr)
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
		mdatab, err := json.MarshalIndent(resp[defaultTargetName].ExporterResponse, "", "  ")
		if err != nil {
			return "", err
		}
		if err := ioutils.AtomicWriteFile(metadataFile, mdatab, 0644); err != nil {
			return "", err
		}
	}

	return resp[defaultTargetName].ExporterResponse["containerimage.digest"], err
}

func getDrivers(ctx context.Context, dockerCli command.Cli, contextPathHash string, addr string) ([]build.DriverInfo, error) {
	imageopt, err := storeutil.GetImageConfig(dockerCli, nil)
	if err != nil {
		return nil, err
	}

	driverOpts := make(map[string]string)
	driverOpts["addr"] = addr

	d, err := driver.GetDriver(ctx, "buildx_buildkit_depot", nil, dockerCli.Client(), imageopt.Auth, nil, nil, nil, driverOpts, nil, contextPathHash)
	if err != nil {
		return nil, err
	}
	return []build.DriverInfo{
		{
			Name:     "default",
			Driver:   d,
			ImageOpt: imageopt,
		},
	}, nil
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
