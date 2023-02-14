package build

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/depot/cli/pkg/buildx"
	_ "github.com/depot/cli/pkg/buildxdriver"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/docker"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/project"
	"github.com/docker/buildx/build"
	"github.com/docker/buildx/util/buildflags"
	"github.com/docker/buildx/util/platformutil"
	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli/command"
	dockerconfig "github.com/docker/cli/cli/config"
	dockeropts "github.com/docker/cli/opts"
	"github.com/docker/go-units"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/moby/buildkit/util/appcontext"
	_ "github.com/moby/buildkit/util/tracing/detect/delegated"
	_ "github.com/moby/buildkit/util/tracing/env"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type buildOptions struct {
	project       string
	token         string
	allowNoOutput bool
	buildPlatform string

	contextPath    string
	dockerfileName string
	printFunc      string

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

func newBuildOptions() buildOptions {
	ulimits := make(map[string]*units.Ulimit)
	return buildOptions{
		ulimits: dockeropts.NewUlimitOpt(&ulimits),
	}
}

func runBuild(dockerCli command.Cli, in buildOptions) (err error) {
	ctx := appcontext.Context()

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

	contexts, err := buildx.ParseContextNames(in.contexts)
	if err != nil {
		return err
	}

	printFunc, err := buildx.ParsePrintFunc(in.printFunc)
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
		BuildArgs:     buildx.ListToMap(in.buildArgs, true),
		ExtraHosts:    in.extraHosts,
		ImageIDFile:   in.imageIDFile,
		Labels:        buildx.ListToMap(in.labels, false),
		NetworkMode:   in.networkMode,
		NoCache:       noCache,
		NoCacheFilter: in.noCacheFilter,
		Pull:          pull,
		ShmSize:       in.shmSize,
		Tags:          in.tags,
		Target:        in.target,
		Ulimits:       in.ulimits,
		PrintFunc:     printFunc,
	}

	platforms, err := platformutil.Parse(in.platforms)
	if err != nil {
		return err
	}
	opts.Platforms = platforms

	dockerConfig := dockerconfig.LoadDefaultConfigFile(os.Stderr)
	opts.Session = append(opts.Session, authprovider.NewDockerAuthProvider(dockerConfig))

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

	imageID, err := buildx.BuildTargets(ctx, dockerCli, map[string]build.Options{buildx.DefaultTargetName: opts}, in.progress, contextPathHash, in.metadataFile, in.project, in.token, in.allowNoOutput, in.buildPlatform)
	err = buildx.WrapBuildError(err, false)
	if err != nil {
		return err
	}

	if in.quiet {
		fmt.Println(imageID)
	}
	return nil
}

func NewCmdBuild() *cobra.Command {
	options := newBuildOptions()

	cmd := &cobra.Command{
		Use:   "build [OPTIONS] PATH | URL | -",
		Short: "Run a Docker build on Depot",
		Args:  cli.ExactArgs(1),
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

			dockerCli, err := docker.NewDockerCLI()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}

			if options.noLoad && options.exportLoad {
				return errors.Errorf("load and no-load may not be both set")
			}

			buildPlatform, err := helpers.NormalizePlatform(options.buildPlatform)
			if err != nil {
				return err
			}
			options.buildPlatform = buildPlatform

			options.contextPath = args[0]
			return runBuild(dockerCli, options)
		},
	}

	var platformsDefault []string
	if v := os.Getenv("DOCKER_DEFAULT_PLATFORM"); v != "" {
		platformsDefault = []string{v}
	}

	flags := cmd.Flags()

	// Depot options
	flags.StringVar(&options.project, "project", "", "Depot project ID")
	flags.StringVar(&options.token, "token", "", "Depot API token")
	flags.StringVar(&options.buildPlatform, "build-platform", "dynamic", `Run builds on this platform ("dynamic", "linux/amd64", "linux/arm64")`)

	allowNoOutput := false
	if v := os.Getenv("DEPOT_SUPPRESS_NO_OUTPUT_WARNING"); v != "" {
		allowNoOutput = true
	}
	flags.BoolVar(&options.allowNoOutput, "suppress-no-output-warning", allowNoOutput, "Suppress warning if no output is generated")
	_ = flags.MarkHidden("suppress-no-output-warning")

	// `docker buildx build` options
	flags.StringSliceVar(&options.extraHosts, "add-host", []string{}, `Add a custom host-to-IP mapping (format: "host:ip")`)
	flags.StringSliceVar(&options.allow, "allow", []string{}, `Allow extra privileged entitlement (e.g., "network.host", "security.insecure")`)
	flags.StringArrayVar(&options.buildArgs, "build-arg", []string{}, "Set build-time variables")
	flags.StringArrayVar(&options.cacheFrom, "cache-from", []string{}, `External cache sources (e.g., "user/app:cache", "type=local,src=path/to/dir")`)
	flags.StringArrayVar(&options.cacheTo, "cache-to", []string{}, `Cache export destinations (e.g., "user/app:cache", "type=local,dest=path/to/dir")`)
	flags.StringVar(&options.cgroupParent, "cgroup-parent", "", "Optional parent cgroup for the container")
	flags.StringArrayVar(&options.contexts, "build-context", []string{}, "Additional build contexts (e.g., name=path)")
	flags.StringVarP(&options.dockerfileName, "file", "f", "", `Name of the Dockerfile (default: "PATH/Dockerfile")`)
	flags.StringVar(&options.imageIDFile, "iidfile", "", "Write the image ID to the file")
	flags.StringArrayVar(&options.labels, "label", []string{}, "Set metadata for an image")
	flags.BoolVar(&options.exportLoad, "load", false, `Shorthand for "--output=type=docker" (default unless --push or --output is specified)`)
	flags.StringVar(&options.networkMode, "network", "default", `Set the networking mode for the "RUN" instructions during build`)
	flags.StringArrayVar(&options.noCacheFilter, "no-cache-filter", []string{}, "Do not cache specified stages")
	flags.StringArrayVarP(&options.outputs, "output", "o", []string{}, `Output destination (format: "type=local,dest=path")`)
	flags.StringArrayVar(&options.platforms, "platform", platformsDefault, "Set target platform for build")
	flags.BoolVar(&options.exportPush, "push", false, `Shorthand for "--output=type=registry"`)
	flags.BoolVarP(&options.quiet, "quiet", "q", false, "Suppress the build output and print image ID on success")
	flags.StringArrayVar(&options.secrets, "secret", []string{}, `Secret to expose to the build (format: "id=mysecret[,src=/local/secret]")`)
	flags.Var(&options.shmSize, "shm-size", `Size of "/dev/shm"`)
	flags.StringArrayVar(&options.ssh, "ssh", []string{}, `SSH agent socket or keys to expose to the build (format: "default|<id>[=<socket>|<key>[,<key>]]")`)
	flags.StringArrayVarP(&options.tags, "tag", "t", []string{}, `Name and optionally a tag (format: "name:tag")`)
	flags.StringVar(&options.target, "target", "", "Set the target build stage to build")
	flags.Var(options.ulimits, "ulimit", "Ulimit options")

	options.noCache = flags.Bool("no-cache", false, "Do not use cache when building the image")
	flags.StringVar(&options.progress, "progress", "auto", `Set type of progress output ("auto", "plain", "tty"). Use plain to show container output`)
	options.pull = flags.Bool("pull", false, "Always attempt to pull all referenced images")
	flags.StringVar(&options.metadataFile, "metadata-file", "", "Write build result metadata to the file")

	if buildx.IsExperimental() {
		flags.StringVar(&options.printFunc, "print", "", "Print result of information request (e.g., outline, targets) [experimental]")
	}

	flags.BoolVar(&options.noLoad, "no-load", false, "Overrides the --load flag")
	_ = flags.MarkHidden("no-load")
	_ = flags.MarkDeprecated("no-load", "--no-load is the default behavior")

	return cmd
}
