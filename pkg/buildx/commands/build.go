// Source: https://github.com/docker/buildx/blob/v0.10/commands/bake.go

package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/containerd/console"
	depotbuild "github.com/depot/cli/pkg/build"
	depotbuildflags "github.com/depot/cli/pkg/buildx/bake/buildflags"
	depotbuildxbuild "github.com/depot/cli/pkg/buildx/build"
	"github.com/depot/cli/pkg/buildx/builder"
	"github.com/depot/cli/pkg/ci"
	"github.com/depot/cli/pkg/cmd/docker"
	"github.com/depot/cli/pkg/debuglog"
	"github.com/depot/cli/pkg/dockerclient"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/load"
	"github.com/depot/cli/pkg/progresshelper"
	"github.com/depot/cli/pkg/registry"
	"github.com/depot/cli/pkg/sbom"
	"github.com/distribution/reference"
	"github.com/docker/buildx/build"
	"github.com/docker/buildx/monitor"
	"github.com/docker/buildx/store"
	"github.com/docker/buildx/store/storeutil"
	"github.com/docker/buildx/util/buildflags"
	"github.com/docker/buildx/util/confutil"
	"github.com/docker/buildx/util/dockerutil"
	"github.com/docker/buildx/util/platformutil"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/buildx/util/tracing"
	"github.com/docker/cli-docs-tool/annotation"
	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli/command"
	dockeropts "github.com/docker/cli/opts"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/go-units"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/morikuni/aec"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/exp/maps"
	"google.golang.org/grpc/codes"
)

const defaultTargetName = "default"

type buildOptions struct {
	contextPath    string
	dockerfileName string
	printFunc      string

	allow         []string
	annotations   []string
	attests       []string
	buildArgs     []string
	cacheFrom     []string
	cacheTo       []string
	cgroupParent  string
	contexts      []string
	extraHosts    []string
	imageIDFile   string
	invoke        string
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
	commonOptions
	DepotOptions
}

type commonOptions struct {
	builder      string
	metadataFile string
	noCache      *bool
	progress     string
	pull         *bool

	exportPush bool
	exportLoad bool

	sbom       string
	provenance string
}

type DepotOptions struct {
	project       string
	token         string
	buildID       string
	buildURL      string
	buildPlatform string
	build         *depotbuild.Build

	save                  bool
	saveTags              []string
	additionalTags        []string
	additionalCredentials []depotbuild.Credential
	loadUsingRegistry     bool
	pullInfo              *depotbuild.PullInfo

	lint       bool
	lintFailOn string

	sbomDir string

	allowNoOutput  bool
	builderOptions []builder.Option
}

func runBuild(dockerCli command.Cli, validatedOpts map[string]build.Options, in buildOptions) (err error) {
	ctx := appcontext.Context()

	ctx, end, err := tracing.TraceCurrentCommand(ctx, "build")
	if err != nil {
		return err
	}
	defer func() {
		end(err)
	}()

	// key string used for kubernetes "sticky" mode
	contextPathHash, err := filepath.Abs(in.contextPath)
	if err != nil {
		contextPathHash = in.contextPath
	}

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

	imageIDs, res, err := buildTargets(ctx, dockerCli, nodes, validatedOpts, in.DepotOptions, in.progress, in.metadataFile, in.exportLoad, in.invoke != "")
	err = wrapBuildError(err, false)
	if err != nil {
		return err
	}

	if in.invoke != "" {
		cfg, err := parseInvokeConfig(in.invoke)
		if err != nil {
			return err
		}
		cfg.ResultCtx = res
		con := console.Current()
		if err := con.SetRaw(); err != nil {
			return errors.Errorf("failed to configure terminal: %v", err)
		}
		err = monitor.RunMonitor(ctx, cfg, func(ctx context.Context) (*build.ResultContext, error) {
			_, rr, err := buildTargets(ctx, dockerCli, nodes, validatedOpts, in.DepotOptions, in.progress, in.metadataFile, false, true)
			return rr, err
		}, io.NopCloser(os.Stdin), nopCloser{os.Stdout}, nopCloser{os.Stderr})
		if err != nil {
			logrus.Warnf("failed to run monitor: %v", err)
		}
		_ = con.Reset()
	}

	if in.quiet {
		for _, imageID := range imageIDs {
			fmt.Println(imageID)
		}
	}
	return nil
}

type nopCloser struct {
	io.WriteCloser
}

func (c nopCloser) Close() error { return nil }

func buildTargets(ctx context.Context, dockerCli command.Cli, nodes []builder.Node, opts map[string]build.Options, depotOpts DepotOptions, progressMode, metadataFile string, exportLoad, allowNoOutput bool) (imageIDs []string, res *build.ResultContext, err error) {
	ctx2, cancel := context.WithCancel(context.TODO())

	printer, err := progress.NewPrinter(ctx2, os.Stderr, os.Stderr, progressMode)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	defer cancel()

	if os.Getenv("DEPOT_NO_SUMMARY_LINK") == "" {
		progress.Write(printer, "[depot] build: "+depotOpts.buildURL, func() error { return err })
	}

	var (
		pullOpts map[string]load.PullOptions
		// Only used for failures to pull images.
		fallbackOpts map[string]build.Options
	)
	if exportLoad {
		fallbackOpts = maps.Clone(opts)
		opts, pullOpts = load.WithDepotImagePull(
			opts,
			load.DepotLoadOptions{
				Project:      depotOpts.project,
				BuildID:      depotOpts.buildID,
				IsBake:       false,
				ProgressMode: progressMode,
				UseRegistry:  depotOpts.loadUsingRegistry,
				PullInfo:     depotOpts.pullInfo,
			},
		)
	}
	if depotOpts.save {
		saveOpts := registry.SaveOptions{
			ProjectID:             depotOpts.project,
			BuildID:               depotOpts.buildID,
			AdditionalTags:        depotOpts.additionalTags,
			AdditionalCredentials: depotOpts.additionalCredentials,
		}
		opts = registry.WithDepotSave(opts, saveOpts)
	}

	buildxNodes := builder.ToBuildxNodes(nodes)
	buildxNodes, err = depotbuildxbuild.FilterAvailableNodes(buildxNodes)
	if err != nil {
		_ = printer.Wait()
		return nil, nil, err
	}
	buildxopts := depotbuildxbuild.BuildxOpts(opts)

	// "Boot" the depot nodes.
	debuglog.Log("booting depot nodes")
	_, clients, err := depotbuildxbuild.ResolveDrivers(ctx, buildxNodes, buildxopts, printer)
	if err != nil {
		_ = printer.Wait()
		return nil, nil, err
	}
	debuglog.Log("booted depot nodes")

	var (
		mu  sync.Mutex
		idx int
	)

	dockerClient := dockerutil.NewClient(dockerCli)
	dockerConfigDir := confutil.ConfigDir(dockerCli)

	linter := NewLinter(printer, NewLintFailureMode(depotOpts.lint, depotOpts.lintFailOn), clients, buildxNodes)

	resp, err := depotbuildxbuild.DepotBuildWithResultHandler(ctx, buildxNodes, opts, dockerClient, dockerConfigDir, printer, linter, func(driverIndex int, gotRes *build.ResultContext) {
		mu.Lock()
		defer mu.Unlock()
		if res == nil || driverIndex < idx {
			idx, res = driverIndex, gotRes
		}
	}, allowNoOutput, depotOpts.build)

	if err != nil {
		// Make sure that the printer has completed before returning failed builds.
		// We ignore the error here as it can only be a context error.
		_ = printer.Wait()

		if errors.Is(err, LintFailed) {
			linter.Print(os.Stderr, progressMode)
		}
		return nil, nil, err
	}

	if metadataFile != "" && resp != nil {
		// DEPOT: Apparently, the build metadata file is a different format than the bake one.
		for _, buildRes := range resp {
			metadata := map[string]interface{}{}
			for _, nodeRes := range buildRes.NodeResponses {
				nodeMetadata := decodeExporterResponse(nodeRes.SolveResponse.ExporterResponse)
				for k, v := range nodeMetadata {
					metadata[k] = v
				}
			}

			if err := writeMetadataFile(metadataFile, depotOpts.project, depotOpts.buildID, nil, metadata, false); err != nil {
				return nil, nil, err
			}
		}
	}

	for _, buildRes := range resp {
		for _, nodeRes := range buildRes.NodeResponses {
			digest := nodeRes.SolveResponse.ExporterResponse[exptypes.ExporterImageDigestKey]
			imageIDs = append(imageIDs, digest)
		}
	}

	if depotOpts.sbomDir != "" {
		err := sbom.Save(ctx, depotOpts.sbomDir, resp)
		if err != nil {
			return nil, nil, err
		}
	}

	// NOTE: the err is returned at the end of this function after the final prints.
	reportingPrinter := progresshelper.NewReporter(ctx, printer, depotOpts.buildID, depotOpts.token)

	if depotOpts.loadUsingRegistry && depotOpts.pullInfo != nil {
		for target, pullOpt := range pullOpts {
			pw := progress.WithPrefix(reportingPrinter, target, len(pullOpts) > 1)
			err = load.PullImages(ctx, dockerCli.Client(), depotOpts.pullInfo.Reference, pullOpt, pw)
			if err != nil {
				break
			}
		}
	} else {
		err = load.DepotFastLoad(ctx, dockerCli.Client(), resp, pullOpts, reportingPrinter)
	}

	if err != nil && !errors.Is(err, context.Canceled) {
		// For now, we will fallback by rebuilding with load.
		if exportLoad {
			// We can only retry if neither the context nor dockerfile are stdin.
			var retryable bool = true
			for _, opt := range opts {
				if opt.Inputs.ContextPath == "-" || opt.Inputs.DockerfilePath == "-" {
					retryable = false
					break
				}
			}

			if retryable {
				progress.Write(reportingPrinter, "[load] fast load failed; retrying", func() error { return err })
				opts = load.WithDockerLoad(fallbackOpts)
				_, err = depotbuildxbuild.DepotBuildWithResultHandler(ctx, buildxNodes, opts, dockerClient, dockerConfigDir, printer, nil, nil, allowNoOutput, depotOpts.build)
			}
		}
	}
	reportingPrinter.Close()

	load.DeleteExportLeases(ctx, resp)

	if err := printer.Wait(); err != nil {
		return nil, nil, err
	}

	printWarnings(os.Stderr, printer.Warnings(), progressMode)
	if depotOpts.save {
		printSaveHelp(depotOpts.project, depotOpts.buildID, progressMode, nil, depotOpts.additionalTags)
	}
	linter.Print(os.Stderr, progressMode)

	for _, buildRes := range resp {
		if opts[buildRes.Name].PrintFunc != nil {
			for _, nodeRes := range buildRes.NodeResponses {
				if err := printResult(opts[buildRes.Name].PrintFunc, nodeRes.SolveResponse.ExporterResponse); err != nil {
					return nil, nil, err
				}
			}
		}
	}

	return imageIDs, res, err
}

func parseInvokeConfig(invoke string) (cfg build.ContainerConfig, err error) {
	cfg.Tty = true
	if invoke == "default" {
		return cfg, nil
	}

	csvReader := csv.NewReader(strings.NewReader(invoke))
	fields, err := csvReader.Read()
	if err != nil {
		return cfg, err
	}
	if len(fields) == 1 && !strings.Contains(fields[0], "=") {
		cfg.Cmd = []string{fields[0]}
		return cfg, nil
	}
	for _, field := range fields {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			return cfg, errors.Errorf("invalid value %s", field)
		}
		key := strings.ToLower(parts[0])
		value := parts[1]
		switch key {
		case "args":
			cfg.Cmd = append(cfg.Cmd, value) // TODO: support JSON
		case "entrypoint":
			cfg.Entrypoint = append(cfg.Entrypoint, value) // TODO: support JSON
		case "env":
			cfg.Env = append(cfg.Env, value)
		case "user":
			cfg.User = &value
		case "cwd":
			cfg.Cwd = &value
		case "tty":
			cfg.Tty, err = strconv.ParseBool(value)
			if err != nil {
				return cfg, errors.Errorf("failed to parse tty: %v", err)
			}
		default:
			return cfg, errors.Errorf("unknown key %q", key)
		}
	}
	return cfg, nil
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

func newBuildOptions() buildOptions {
	ulimits := make(map[string]*units.Ulimit)
	return buildOptions{
		ulimits: dockeropts.NewUlimitOpt(&ulimits),
	}
}

func validateBuildOptions(in *buildOptions) (map[string]build.Options, error) {
	noCache := false
	if in.noCache != nil {
		noCache = *in.noCache
	}
	pull := false
	if in.pull != nil {
		pull = *in.pull
	}

	if noCache && len(in.noCacheFilter) > 0 {
		return nil, errors.Errorf("--no-cache and --no-cache-filter cannot currently be used together")
	}

	if in.quiet && in.progress != progress.PrinterModeAuto && in.progress != progress.PrinterModeQuiet {
		return nil, errors.Errorf("progress=%s and quiet cannot be used together", in.progress)
	} else if in.quiet {
		in.progress = "quiet"
	}

	_, isCI := ci.Provider()
	if in.progress == progress.PrinterModeAuto && isCI {
		in.progress = progress.PrinterModePlain
	}

	contexts, err := parseContextNames(in.contexts)
	if err != nil {
		return nil, err
	}

	printFunc, err := parsePrintFunc(in.printFunc)
	if err != nil {
		return nil, err
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
		PrintFunc:     printFunc,
	}

	platforms, err := platformutil.Parse(in.platforms)
	if err != nil {
		return nil, err
	}
	opts.Platforms = platforms

	opts.Session = append(opts.Session, registry.NewDockerAuthProviderWithDepotAuth())

	secrets, err := buildflags.ParseSecretSpecs(in.secrets)
	if err != nil {
		return nil, err
	}
	opts.Session = append(opts.Session, secrets)

	sshSpecs := in.ssh
	if len(sshSpecs) == 0 && buildflags.IsGitSSH(in.contextPath) {
		sshSpecs = []string{"default"}
	}
	ssh, err := buildflags.ParseSSHSpecs(sshSpecs)
	if err != nil {
		return nil, err
	}
	opts.Session = append(opts.Session, ssh)

	outputs, err := buildflags.ParseOutputs(in.outputs)
	if err != nil {
		return nil, err
	}
	if in.exportPush {
		if len(outputs) == 0 {
			outputs = []client.ExportEntry{{
				Type: "image",
				Attrs: map[string]string{
					"push":                       "true",
					"depot.export.image.version": "2",
				},
			}}
		} else {
			switch outputs[0].Type {
			case "image":
				outputs[0].Attrs["push"] = "true"
				outputs[0].Attrs["depot.export.image.version"] = "2"
			default:
				return nil, errors.Errorf("push and %q output can't be used together", outputs[0].Type)
			}
		}
	}

	// When using --save without explicit exports, create an image export
	// so that annotations can be applied to it
	if in.save && len(outputs) == 0 {
		outputs = []client.ExportEntry{{
			Type: "image",
			Attrs: map[string]string{
				"depot.export.image.version": "2",
			},
		}}
	}

	opts.Exports = outputs

	// parse and apply annotations to exports
	if len(in.annotations) > 0 {
		annotations, err := depotbuildflags.ParseAnnotations(in.annotations)
		if err != nil {
			return nil, errors.Wrap(err, "parse annotations")
		}

		for i := range opts.Exports {
			if opts.Exports[i].Attrs == nil {
				opts.Exports[i].Attrs = make(map[string]string)
			}
			for k, v := range annotations {
				opts.Exports[i].Attrs[k.String()] = v
			}
		}
	}

	inAttests := append([]string{}, in.attests...)
	if in.provenance != "" {
		inAttests = append(inAttests, buildflags.CanonicalizeAttest("provenance", in.provenance))
	}
	if in.sbom != "" {
		inAttests = append(inAttests, buildflags.CanonicalizeAttest("sbom", in.sbom))
	}
	opts.Attests, err = buildflags.ParseAttests(inAttests)
	if err != nil {
		return nil, err
	}

	cacheImports, err := buildflags.ParseCacheEntry(in.cacheFrom)
	if err != nil {
		return nil, err
	}
	opts.CacheFrom = cacheImports

	cacheExports, err := buildflags.ParseCacheEntry(in.cacheTo)
	if err != nil {
		return nil, err
	}
	opts.CacheTo = cacheExports

	allow, err := buildflags.ParseEntitlements(in.allow)
	if err != nil {
		return nil, err
	}
	opts.Allow = allow

	return map[string]build.Options{defaultTargetName: opts}, nil
}

func BuildCmd() *cobra.Command {
	options := newBuildOptions()

	cmd := &cobra.Command{
		Use:     "build [OPTIONS] PATH | URL | -",
		Aliases: []string{"b"},
		Short:   "Start a build",
		Args:    cli.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dockerCli, err := dockerclient.NewDockerCLI()
			if err != nil {
				return err
			}

			options.contextPath = args[0]
			cmd.Flags().VisitAll(checkWarnedFlags)

			token, err := helpers.ResolveProjectAuth(context.Background(), options.token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			options.project = helpers.ResolveProjectID(options.project, options.contextPath, options.dockerfileName)

			buildPlatform, err := helpers.ResolveBuildPlatform(options.buildPlatform)
			if err != nil {
				return err
			}

			validatedOpts, err := validateBuildOptions(&options)
			if err != nil {
				return err
			}

			req := helpers.NewBuildRequest(
				options.project,
				validatedOpts,
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

			ctxDriverUpdate, driverUpdateCancel := context.WithCancel(cmd.Context())
			go func() {
				// Optimistically update drivers in the background.
				// This helps to keep the drivers up-to-date.
				_ = docker.UpdateDrivers(ctxDriverUpdate, dockerCli)
			}()

			var buildErr error
			defer func() {
				driverUpdateCancel()
				build.Finish(buildErr)
				PrintBuildURL(build.BuildURL, options.progress)
			}()

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

			buildErr = retryRetryableErrors(context.Background(), func() error {
				return runBuild(dockerCli, validatedOpts, options)
			})
			return rewriteFriendlyErrors(buildErr)
		},
	}

	var platformsDefault []string
	if v := os.Getenv("DOCKER_DEFAULT_PLATFORM"); v != "" {
		platformsDefault = []string{v}
	}

	flags := cmd.Flags()

	flags.StringSliceVar(&options.extraHosts, "add-host", []string{}, `Add a custom host-to-IP mapping (format: "host:ip")`)
	_ = flags.SetAnnotation("add-host", annotation.ExternalURL, []string{"https://docs.docker.com/engine/reference/commandline/build/#add-host"})

	flags.StringSliceVar(&options.allow, "allow", []string{}, `Allow extra privileged entitlement (e.g., "network.host", "security.insecure")`)

	flags.StringArrayVarP(&options.annotations, "annotation", "", []string{}, "Add annotation to the image")

	flags.StringArrayVar(&options.buildArgs, "build-arg", []string{}, "Set build-time variables")

	flags.StringArrayVar(&options.cacheFrom, "cache-from", []string{}, `External cache sources (e.g., "user/app:cache", "type=local,src=path/to/dir")`)

	flags.StringArrayVar(&options.cacheTo, "cache-to", []string{}, `Cache export destinations (e.g., "user/app:cache", "type=local,dest=path/to/dir")`)

	flags.StringVar(&options.cgroupParent, "cgroup-parent", "", "Optional parent cgroup for the container")
	_ = flags.SetAnnotation("cgroup-parent", annotation.ExternalURL, []string{"https://docs.docker.com/engine/reference/commandline/build/#cgroup-parent"})

	flags.StringArrayVar(&options.contexts, "build-context", []string{}, "Additional build contexts (e.g., name=path)")

	flags.StringVarP(&options.dockerfileName, "file", "f", "", `Name of the Dockerfile (default: "PATH/Dockerfile")`)
	_ = flags.SetAnnotation("file", annotation.ExternalURL, []string{"https://docs.docker.com/engine/reference/commandline/build/#file"})

	flags.StringVar(&options.imageIDFile, "iidfile", "", "Write the image ID to the file")

	flags.StringArrayVar(&options.labels, "label", []string{}, "Set metadata for an image")

	flags.BoolVar(&options.exportLoad, "load", false, `Shorthand for "--output=type=docker"`)

	flags.StringVar(&options.networkMode, "network", "default", `Set the networking mode for the "RUN" instructions during build`)

	flags.StringArrayVar(&options.noCacheFilter, "no-cache-filter", []string{}, "Do not cache specified stages")

	flags.StringArrayVarP(&options.outputs, "output", "o", []string{}, `Output destination (format: "type=local,dest=path")`)

	flags.StringArrayVar(&options.platforms, "platform", platformsDefault, "Set target platform for build")

	if isExperimental() {
		flags.StringVar(&options.printFunc, "print", "", "Print result of information request (e.g., outline, targets) [experimental]")
	}

	flags.BoolVar(&options.exportPush, "push", false, `Shorthand for "--output=type=registry"`)

	flags.BoolVarP(&options.quiet, "quiet", "q", false, "Suppress the build output and print image ID on success")

	flags.StringArrayVar(&options.secrets, "secret", []string{}, `Secret to expose to the build (format: "id=mysecret[,src=/local/secret]")`)

	flags.Var(&options.shmSize, "shm-size", `Size of "/dev/shm"`)

	flags.StringArrayVar(&options.ssh, "ssh", []string{}, `SSH agent socket or keys to expose to the build (format: "default|<id>[=<socket>|<key>[,<key>]]")`)

	flags.StringArrayVarP(&options.tags, "tag", "t", []string{}, `Name and optionally a tag (format: "name:tag")`)
	_ = flags.SetAnnotation("tag", annotation.ExternalURL, []string{"https://docs.docker.com/engine/reference/commandline/build/#tag"})

	flags.StringVar(&options.target, "target", "", "Set the target build stage to build")
	_ = flags.SetAnnotation("target", annotation.ExternalURL, []string{"https://docs.docker.com/engine/reference/commandline/build/#target"})

	flags.Var(options.ulimits, "ulimit", "Ulimit options")

	flags.StringArrayVar(&options.attests, "attest", []string{}, `Attestation parameters (format: "type=sbom,generator=image")`)
	flags.StringVar(&options.sbom, "sbom", "", `Shorthand for "--attest=type=sbom"`)
	flags.StringVar(&options.provenance, "provenance", "", `Shortand for "--attest=type=provenance"`)

	if isExperimental() {
		flags.StringVar(&options.invoke, "invoke", "", "Invoke a command after the build [experimental]")
	}

	// hidden flags
	var ignore string
	var ignoreSlice []string
	var ignoreBool bool
	var ignoreInt int64

	flags.BoolVar(&ignoreBool, "compress", false, "Compress the build context using gzip")
	_ = flags.MarkHidden("compress")

	flags.StringVar(&ignore, "isolation", "", "Container isolation technology")
	_ = flags.MarkHidden("isolation")
	_ = flags.SetAnnotation("isolation", "flag-warn", []string{"isolation flag is deprecated with BuildKit."})

	flags.StringSliceVar(&ignoreSlice, "security-opt", []string{}, "Security options")
	_ = flags.MarkHidden("security-opt")
	_ = flags.SetAnnotation("security-opt", "flag-warn", []string{`security-opt flag is deprecated. "RUN --security=insecure" should be used with BuildKit.`})

	flags.BoolVar(&ignoreBool, "squash", false, "Squash newly built layers into a single new layer")
	_ = flags.MarkHidden("squash")
	_ = flags.SetAnnotation("squash", "flag-warn", []string{"experimental flag squash is removed with BuildKit. You should squash inside build using a multi-stage Dockerfile for efficiency."})

	flags.StringVarP(&ignore, "memory", "m", "", "Memory limit")
	_ = flags.MarkHidden("memory")

	flags.StringVar(&ignore, "memory-swap", "", `Swap limit equal to memory plus swap: "-1" to enable unlimited swap`)
	_ = flags.MarkHidden("memory-swap")

	flags.Int64VarP(&ignoreInt, "cpu-shares", "c", 0, "CPU shares (relative weight)")
	_ = flags.MarkHidden("cpu-shares")

	flags.Int64Var(&ignoreInt, "cpu-period", 0, "Limit the CPU CFS (Completely Fair Scheduler) period")
	_ = flags.MarkHidden("cpu-period")

	flags.Int64Var(&ignoreInt, "cpu-quota", 0, "Limit the CPU CFS (Completely Fair Scheduler) quota")
	_ = flags.MarkHidden("cpu-quota")

	flags.StringVar(&ignore, "cpuset-cpus", "", `CPUs in which to allow execution ("0-3", "0,1")`)
	_ = flags.MarkHidden("cpuset-cpus")

	flags.StringVar(&ignore, "cpuset-mems", "", `MEMs in which to allow execution ("0-3", "0,1")`)
	_ = flags.MarkHidden("cpuset-mems")

	flags.BoolVar(&ignoreBool, "rm", true, "Remove intermediate containers after a successful build")
	_ = flags.MarkHidden("rm")

	flags.BoolVar(&ignoreBool, "force-rm", false, "Always remove intermediate containers")
	_ = flags.MarkHidden("force-rm")

	commonBuildFlags(&options.commonOptions, flags)
	depotFlags(cmd, &options.DepotOptions, flags)
	depotRegistryFlags(cmd, &options.DepotOptions, flags)
	return cmd
}

func commonBuildFlags(options *commonOptions, flags *pflag.FlagSet) {
	options.noCache = flags.Bool("no-cache", false, "Do not use cache when building the image")
	flags.StringVar(&options.progress, "progress", "auto", `Set type of progress output ("auto", "plain", "tty"). Use plain to show container output`)
	options.pull = flags.Bool("pull", false, "Always attempt to pull all referenced images")
	flags.StringVar(&options.metadataFile, "metadata-file", "", "Write build result metadata to the file")
}

func depotFlags(cmd *cobra.Command, options *DepotOptions, flags *pflag.FlagSet) {
	depotBuildFlags(options, flags)
	depotLintFlags(cmd, options, flags)
	depotAttestationFlags(cmd, options, flags)
}

func depotBuildFlags(options *DepotOptions, flags *pflag.FlagSet) {
	flags.StringVar(&options.project, "project", "", "Depot project ID")
	flags.StringVar(&options.token, "token", "", "Depot token")
	flags.StringVar(&options.buildPlatform, "build-platform", "dynamic", `Run builds on this platform ("dynamic", "linux/amd64", "linux/arm64")`)

	allowNoOutput := false
	if v := os.Getenv("DEPOT_SUPPRESS_NO_OUTPUT_WARNING"); v != "" {
		allowNoOutput = true
	}
	flags.BoolVar(&options.allowNoOutput, "suppress-no-output-warning", allowNoOutput, "Suppress warning if no output is generated")
	_ = flags.MarkHidden("suppress-no-output-warning")
}

func depotLintFlags(cmd *cobra.Command, options *DepotOptions, flags *pflag.FlagSet) {
	flags.BoolVar(&options.lint, "lint", false, `Lint Dockerfiles`)
	flags.StringVar(&options.lintFailOn, "lint-fail-on", "error", `controls lint severity that fails the build ("info", "warn", "error", "none")`)
	_ = cmd.RegisterFlagCompletionFunc("lint-fail-on", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"error\tFail on errors [default]",
			"warn\tFail on errors and warnings",
			"info\tFail on all lint issues",
			"none\tLint issues do not fail the build",
		}, cobra.ShellCompDirectiveDefault
	})
}

func depotAttestationFlags(_ *cobra.Command, options *DepotOptions, flags *pflag.FlagSet) {
	flags.StringVar(&options.sbomDir, "sbom-dir", "", `directory to store SBOM attestations`)
}

func depotRegistryFlags(_ *cobra.Command, options *DepotOptions, flags *pflag.FlagSet) {
	flags.BoolVar(&options.save, "save", false, `Saves the build to the depot registry`)
	flags.StringArrayVar(&options.saveTags, "save-tag", []string{}, `Additional custom tag for the saved image, use with --save`)
}

func checkWarnedFlags(f *pflag.Flag) {
	if !f.Changed {
		return
	}
	for t, m := range f.Annotations {
		switch t {
		case "flag-warn":
			logrus.Warn(m[0])
		}
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

func parsePrintFunc(str string) (*build.PrintFunc, error) {
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

func writeMetadataFile(filename, projectID, buildID string, requestedTargets []string, metadata map[string]interface{}, isBake bool) error {
	depotBuild := struct {
		BuildID   string   `json:"buildID"`
		ProjectID string   `json:"projectID"`
		Targets   []string `json:"targets,omitempty"`
	}{
		BuildID:   buildID,
		ProjectID: projectID,
	}

	if isBake {
		// If requestedTargets was provided, use that; otherwise use all metadata keys
		if len(requestedTargets) > 0 {
			depotBuild.Targets = requestedTargets
		} else {
			depotBuild.Targets = maps.Keys(metadata)
		}
	}

	metadata["depot.build"] = depotBuild
	b, err := json.MarshalIndent(metadata, "", "  ")
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
		if k == load.ImagesExported {
			_, manifests, imageConfigs, err := load.DecodeExportImages(v)
			if err != nil {
				out[k] = v
			} else {
				out["manifests"] = manifests
				out["imageConfigs"] = imageConfigs
			}

			continue
		}

		// Filter out the SBOMs as they can be quite large.
		if k == sbom.SBOMsLabel {
			continue
		}

		var raw map[string]interface{}
		if err = json.Unmarshal(dt, &raw); err != nil || len(raw) == 0 {
			out[k] = v
			continue
		}
		// DEPOT: Remove the depot specific keys.
		// We use these for fast load and the format is not compatible with the OCI spec.
		if k == exptypes.ExporterImageDescriptorKey {
			if anno, ok := raw["annotations"]; ok {
				if anno, ok := anno.(map[string]interface{}); ok {
					delete(anno, "depot.containerimage.index")
					delete(anno, "depot.containerimage.config")
					delete(anno, "depot.containerimage.manifest")
					out[k] = raw
					continue
				}
			}
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
		if st.Code() == codes.Unavailable && strings.Contains(st.Message(), "keepalive ping failed") {
			msg := "Connection to BuildKit lost. This error occurs when BuildKit is shut down, often due to the runner reaching resource starvation caused by an Out of Memory (OOM) condition.\n\nFor troubleshooting steps, visit: https://depot.dev/docs/container-builds/troubleshooting#error-keep-alive-ping-failed-to-receive-ack-within-timeout"
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

func retryRetryableErrors(ctx context.Context, f func() error) error {
	maxRetryCountEnv := os.Getenv("DEPOT_BUILDKIT_ERROR_MAX_RETRY_COUNT")
	maxRetryCount := 5
	if maxRetryCountEnv != "" {
		maxRetryCount, _ = strconv.Atoi(maxRetryCountEnv)
	}

	retryCount := 0
	for {
		err := f()
		if !shouldRetryError(err) {
			return err
		}
		if retryCount >= maxRetryCount {
			return err
		}
		retryCount++
		fmt.Printf("\nReceived retryable BuildKit error, retrying: %v\n", err)
		fmt.Println()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func shouldRetryError(err error) bool {
	if err == nil {
		return false
	}

	if strings.Contains(err.Error(), "inconsistent graph state") {
		return true
	}

	if strings.Contains(err.Error(), "failed to get state for index") {
		return true
	}

	return false
}

func rewriteFriendlyErrors(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "header key \"exclude-patterns\" contains value with non-printable ASCII characters") {
		return errors.New(err.Error() + ". Please check your .dockerignore file for invalid characters.")
	}
	if strings.Contains(err.Error(), "failed to calculate checksum of ref") {
		pattern := `failed to solve: failed to compute cache key: failed to calculate checksum of ref [^:]+::[^:]+:`
		re := regexp.MustCompile(pattern)

		simplified := re.ReplaceAllString(err.Error(), "")
		return errors.New(simplified + ". Please check if the files exist in the context.")
	}
	if strings.Contains(err.Error(), "code = Canceled desc = grpc: the client connection is closing") {
		return errors.New("build canceled")
	}
	if strings.Contains(err.Error(), "keepalive ping failed") {
		return errors.New("Connection to BuildKit lost. This error occurs when BuildKit is shut down, often due to the runner reaching resource starvation caused by an Out of Memory (OOM) condition.\n\nFor troubleshooting steps, visit: https://depot.dev/docs/container-builds/troubleshooting#error-keep-alive-ping-failed-to-receive-ack-within-timeout")
	}
	return err
}

func isExperimental() bool {
	if v, ok := os.LookupEnv("BUILDX_EXPERIMENTAL"); ok {
		vv, _ := strconv.ParseBool(v)
		return vv
	}
	return false
}

func updateLastActivity(dockerCli command.Cli, ng *store.NodeGroup) error {
	txn, release, err := storeutil.GetStore(dockerCli)
	if err != nil {
		return err
	}
	defer release()
	return txn.UpdateLastActivity(ng)
}
