package commands

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/containerd/platforms"
	depotbuild "github.com/depot/cli/pkg/build"
	"github.com/depot/cli/pkg/buildxdriver"
	"github.com/depot/cli/pkg/compose"
	"github.com/depot/cli/pkg/dockerclient"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/load"
	"github.com/depot/cli/pkg/progresshelper"
	"github.com/depot/cli/pkg/registry"
	"github.com/depot/cli/pkg/sbom"
	"github.com/docker/buildx/bake"
	"github.com/docker/buildx/build"
	"github.com/docker/buildx/builder"
	"github.com/docker/buildx/util/buildflags"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/buildx/util/urlutil"
	"github.com/docker/cli/cli/command"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

type BakeOptions struct {
	files            []string
	overrides        []string
	printOnly        bool
	requestedProject string
	commonOptions
	DepotOptions
}

func RunBake(dockerCli command.Cli, in BakeOptions, validator BakeValidator, printer *progresshelper.SharedPrinter) (linter *Linter, requestedTargets []string, err error) {
	ctx := appcontext.Context()

	if os.Getenv("DEPOT_NO_SUMMARY_LINK") == "" && os.Getenv("DEPOT_IN_AUTOMATION") == "" {
		_ = progress.Write(printer, "[depot] build: "+in.buildURL, func() error { return err })
	}

	nodes := buildxdriver.Nodes(dockerCli, in.build, in.buildPlatform)

	validatedOpts, requestedTargets, err := validator.Validate(ctx, nodes, printer)
	if err != nil {
		return nil, nil, err
	}

	buildOpts := validatedOpts.ProjectOpts(in.project)
	if buildOpts == nil && in.requestedProject != "" {
		validatedOpts = validatedOpts.WithResolvedProjectID(in.requestedProject, in.project)
		buildOpts = validatedOpts.ProjectOpts(in.project)
	}
	if buildOpts == nil {
		return nil, nil, fmt.Errorf("project %s build options not found", in.project)
	}
	buildOpts = cloneOptions(buildOpts)

	projectRequestedTargets := make([]string, 0, len(requestedTargets))
	for _, target := range requestedTargets {
		if _, exists := buildOpts[target]; exists {
			projectRequestedTargets = append(projectRequestedTargets, target)
		}
	}
	requestedTargets = projectRequestedTargets

	var targetsToLoad []string
	for target, opts := range buildOpts {
		shouldLoad := !slices.ContainsFunc(opts.Exports, func(e client.ExportEntry) bool { return e.Type == "cacheonly" })
		if in.exportLoad {
			shouldLoad = shouldLoad && slices.Contains(requestedTargets, target)
		}
		if shouldLoad {
			targetsToLoad = append(targetsToLoad, target)
		}
	}

	var (
		pullOpts     map[string]load.PullOptions
		fallbackOpts map[string]build.Options
	)
	if in.exportLoad {
		fallbackOpts = cloneOptions(buildOpts)
		buildOpts, pullOpts = load.WithDepotImagePull(buildOpts, load.DepotLoadOptions{
			Project:       in.project,
			BuildID:       in.buildID,
			IsBake:        true,
			ProgressMode:  in.progress,
			UseRegistry:   in.loadUsingRegistry,
			PullInfo:      in.pullInfo,
			BuildPlatform: in.buildPlatform,
		})
	}
	if in.save {
		buildOpts = registry.WithDepotSave(buildOpts, registry.SaveOptions{
			ProjectID:             in.project,
			BuildID:               in.buildID,
			AdditionalTags:        in.additionalTags,
			AdditionalCredentials: in.additionalCredentials,
			AddTargetSuffix:       true,
			RequestedTargets:      requestedTargets,
		})
	}

	linter = NewLinter(printer, NewLintFailureMode(in.lint, in.lintFailOn), nodes)
	resp, err := executeBuild(ctx, dockerCli, nodes, buildOpts, printer, linter, nil)
	if err != nil {
		if errors.Is(err, LintFailed) {
			linter.Print(os.Stderr, in.progress)
		}
		return nil, nil, wrapBuildError(err, true)
	}

	if in.metadataFile != "" {
		dt := make(map[string]any)
		for _, buildRes := range resp.targets {
			metadata := map[string]any{}
			for _, nodeRes := range buildRes.NodeResponses {
				maps.Copy(metadata, decodeExporterResponse(nodeRes.SolveResponse.ExporterResponse))
			}
			if len(metadata) > 0 {
				dt[buildRes.Name] = metadata
			}
		}
		if err := writeMetadataFile(in.metadataFile, in.project, in.buildID, requestedTargets, dt, true); err != nil {
			return nil, nil, err
		}
	}

	if in.sbomDir != "" {
		if err := sbom.Save(ctx, in.sbomDir, resp.targets); err != nil {
			return nil, nil, err
		}
	}

	if len(pullOpts) > 0 {
		eg, ctx2 := errgroup.WithContext(ctx)
		eg.SetLimit(3)
		for _, target := range resp.targets {
			eg.Go(func() error {
				targetResponses := []buildxdriver.TargetResponse{target}
				var err error
				if slices.Contains(targetsToLoad, target.Name) {
					reportingPrinter := progresshelper.NewReporter(ctx2, printer, in.buildID, in.token)
					defer reportingPrinter.Close()

					if in.loadUsingRegistry && in.pullInfo != nil {
						if pullOpt, ok := pullOpts[target.Name]; ok {
							pw := progress.WithPrefix(reportingPrinter, target.Name, len(pullOpts) > 1)
							err = load.PullImages(ctx, dockerCli.Client(), fmt.Sprintf("%s-%s", in.pullInfo.Reference, target.Name), pullOpt, pw)
						}
					} else {
						err = load.DepotFastLoad(ctx2, dockerCli.Client(), targetResponses, pullOpts, reportingPrinter)
					}
				}
				load.DeleteExportLeases(ctx2, targetResponses)
				return err
			})
		}

		err = eg.Wait()
		if err != nil && !errors.Is(err, context.Canceled) {
			if in.exportLoad {
				_ = progress.Write(printer, "[load] fast load failed; retrying", func() error { return err })
				_, err = executeBuild(ctx, dockerCli, nodes, load.WithSelectiveDockerLoad(fallbackOpts, targetsToLoad), printer, nil, nil)
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

			for idx, file := range options.files {
				options.files[idx] = strings.TrimPrefix(file, "cwd://")
			}

			if options.printOnly {
				if isRemoteTarget(args) {
					return errors.New("cannot use remote target with --print")
				}
				return BakePrint(dockerCli, args, options)
			}

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
			options.requestedProject = options.project

			buildPlatform, err := helpers.ResolveBuildPlatform(options.buildPlatform)
			if err != nil {
				return err
			}
			options.buildPlatform = buildPlatform

			var (
				validator  BakeValidator
				projectIDs []string
				projects   *projectBuildOptions
			)
			if isRemoteTarget(args) {
				validator = NewRemoteBakeValidator(options, args)
				projectIDs = []string{options.project}
			} else {
				validator = NewLocalBakeValidator(options, args)
				projects, _, err = validator.Validate(context.Background(), nil, nil)
				if err != nil {
					return err
				}
				projectIDs = projects.ProjectIDs()
			}

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
			type bakeResult struct {
				linter           *Linter
				requestedTargets []string
				saveInfo         *saveInfo
			}
			var (
				mu          sync.Mutex
				bakeResults []bakeResult
			)

			eg, ctx := errgroup.WithContext(context.Background())
			for _, projectID := range projectIDs {
				options.project = projectID
				options.requestedProject = projectID

				var bakeOpts map[string]build.Options
				if projects != nil {
					bakeOpts = projects.ProjectOpts(projectID)
				}
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

				if buildProject := build.BuildProject(); buildProject != "" {
					options.project = buildProject
				}
				if options.exportLoad && build.LoadUsingRegistry() {
					options.save = true
					if pullInfo, err := depotbuild.PullBuildInfo(context.Background(), build.ID, token); err == nil {
						options.loadUsingRegistry = true
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

				o := options
				eg.Go(func() error {
					var (
						linter           *Linter
						requestedTargets []string
					)
					buildErr := retryRetryableErrors(ctx, func() error {
						var err error
						linter, requestedTargets, err = RunBake(dockerCli, o, validator, printer)
						return err
					})
					if buildErr != nil {
						buildErr = rewriteFriendlyErrors(buildErr)
					}

					o.build.Finish(buildErr)
					PrintBuildURL(o.buildURL, o.progress)

					if buildErr == nil {
						result := bakeResult{linter: linter, requestedTargets: requestedTargets}
						if o.save {
							result.saveInfo = &saveInfo{project: o.project, buildID: o.buildID, additionalTags: o.additionalTags}
						}
						mu.Lock()
						bakeResults = append(bakeResults, result)
						mu.Unlock()
					}
					return buildErr
				})
			}

			err = eg.Wait()

			for range projectIDs {
				_ = printer.Wait()
			}

			for _, result := range bakeResults {
				if result.saveInfo != nil {
					printSaveHelp(result.saveInfo.project, result.saveInfo.buildID, options.progress, result.requestedTargets, result.saveInfo.additionalTags)
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
	overrides := slices.Clone(in.overrides)
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
	return len(targets) > 0 && urlutil.IsRemoteURL(targets[0])
}

func bakeDefaults(cmdContext string) map[string]string {
	return map[string]string{
		"BAKE_CMD_CONTEXT":    cmdContext,
		"BAKE_LOCAL_PLATFORM": platforms.Format(platforms.DefaultSpec()),
	}
}

// projectBuildOptions holds the build options of each target, grouped by
// the Depot project that builds the target.
type projectBuildOptions struct {
	projectTargetOptions map[string]map[string]build.Options
}

func newProjectBuildOptions(defaultProjectID string, opts map[string]build.Options, targetProjects map[string]string) (*projectBuildOptions, error) {
	p := &projectBuildOptions{projectTargetOptions: map[string]map[string]build.Options{}}
	for targetName, opt := range opts {
		projectID := targetProjects[targetName]
		if projectID == "" {
			projectID = defaultProjectID
		}
		if projectID == "" {
			return nil, errors.Errorf("Project ID is missing for target %s, please specify with --project, DEPOT_PROJECT_ID, or run `depot init`", targetName)
		}
		if _, ok := p.projectTargetOptions[projectID]; !ok {
			p.projectTargetOptions[projectID] = map[string]build.Options{}
		}
		p.projectTargetOptions[projectID][targetName] = opt
	}
	return p, nil
}

func (p *projectBuildOptions) ProjectOpts(id string) map[string]build.Options {
	return p.projectTargetOptions[id]
}

func (p *projectBuildOptions) ProjectIDs() []string {
	return slices.Sorted(maps.Keys(p.projectTargetOptions))
}

// WithResolvedProjectID returns a copy of the options keyed by the project
// that the API resolved. Builds of other projects keep the original options.
func (p *projectBuildOptions) WithResolvedProjectID(requestedID, resolvedID string) *projectBuildOptions {
	if p == nil || resolvedID == "" || requestedID == "" || requestedID == resolvedID {
		return p
	}
	projectOpts, ok := p.projectTargetOptions[requestedID]
	if !ok {
		return p
	}
	if _, ok := p.projectTargetOptions[resolvedID]; ok {
		return p
	}
	projectTargetOptions := maps.Clone(p.projectTargetOptions)
	projectTargetOptions[resolvedID] = projectOpts
	delete(projectTargetOptions, requestedID)
	return &projectBuildOptions{projectTargetOptions: projectTargetOptions}
}

// BakeValidator returns the build options of each target and the targets
// that the user requested.
type BakeValidator interface {
	Validate(ctx context.Context, nodes []builder.Node, pw progress.Writer) (opts *projectBuildOptions, targets []string, err error)
}

var (
	_ BakeValidator = (*RemoteBakeValidator)(nil)
	_ BakeValidator = (*LocalBakeValidator)(nil)
)

type LocalBakeValidator struct {
	options     BakeOptions
	bakeTargets bakeTargets

	once      sync.Once
	buildOpts *projectBuildOptions
	targets   []string
	err       error
}

func NewLocalBakeValidator(options BakeOptions, args []string) *LocalBakeValidator {
	return &LocalBakeValidator{options: options, bakeTargets: parseBakeTargets(args)}
}

// Validate reads the local definition once, because a definition on
// standard input can only be read once.
func (t *LocalBakeValidator) Validate(ctx context.Context, _ []builder.Node, _ progress.Writer) (*projectBuildOptions, []string, error) {
	t.once.Do(func() {
		files, err := bake.ReadLocalFiles(t.options.files, os.Stdin, nil)
		if err != nil {
			t.err = err
			return
		}
		if len(files) == 0 {
			t.err = errors.New("couldn't find a bake definition")
			return
		}
		t.buildOpts, t.targets, t.err = readBakeTargets(ctx, files, nil, t.options, t.bakeTargets, true)
	})
	return t.buildOpts, t.targets, t.err
}

type RemoteBakeValidator struct {
	options     BakeOptions
	bakeTargets bakeTargets
}

func NewRemoteBakeValidator(options BakeOptions, args []string) *RemoteBakeValidator {
	return &RemoteBakeValidator{options: options, bakeTargets: parseBakeTargets(args)}
}

func (t *RemoteBakeValidator) Validate(ctx context.Context, nodes []builder.Node, pw progress.Writer) (*projectBuildOptions, []string, error) {
	files, inp, err := bake.ReadRemoteFiles(ctx, nodes, t.bakeTargets.FileURL, t.options.files, pw)
	if err != nil {
		return nil, nil, err
	}
	projects, requestedTargets, err := readBakeTargets(ctx, files, inp, t.options, t.bakeTargets, false)
	if err != nil {
		return nil, nil, err
	}
	// The build of a remote definition starts before the definition is
	// read, so it can build targets of only one project.
	for _, projectID := range projects.ProjectIDs() {
		if projectID == t.options.project {
			continue
		}
		target := slices.Sorted(maps.Keys(projects.ProjectOpts(projectID)))[0]
		return nil, nil, errors.Errorf("target %s sets project_id %s, but a remote bake definition can build targets of only one project (%s); run bake with a local copy of the definition to build several projects", target, projectID, t.options.project)
	}
	return projects, requestedTargets, nil
}

// readBakeTargets resolves the requested targets of a bake definition into
// build options grouped by Depot project, and returns the targets that the
// requested targets and groups expand to.
func readBakeTargets(ctx context.Context, files []bake.File, inp *bake.Input, options BakeOptions, bakeTargets bakeTargets, defaultComposeTags bool) (*projectBuildOptions, []string, error) {
	defaults := bakeDefaults(bakeTargets.CmdContext)
	buildFiles := withoutProjectIDs(files)

	targets, _, err := bake.ReadTargets(ctx, buildFiles, bakeTargets.Targets, overrides(options), defaults, nil, &bake.EntitlementConf{})
	if err != nil {
		return nil, nil, err
	}

	cfg, _, err := bake.ParseFiles(buildFiles, defaults, nil)
	if err != nil {
		return nil, nil, err
	}
	resolved := map[string]struct{}{}
	for _, target := range bakeTargets.Targets {
		names, _ := cfg.ResolveGroup(target)
		for _, name := range names {
			if _, ok := targets[name]; ok {
				resolved[name] = struct{}{}
			}
		}
	}
	requestedTargets := slices.Sorted(maps.Keys(resolved))

	composeTargets, err := compose.Targets(files)
	if err != nil {
		return nil, nil, err
	}
	if defaultComposeTags {
		for name, target := range targets {
			if ct, ok := composeTargets[name]; ok && len(target.Tags) == 0 {
				target.Tags = ct.Tags
			}
		}
	}

	opts, err := bake.TargetsToBuildOpt(targets, inp)
	if err != nil {
		return nil, nil, err
	}
	for name, opt := range opts {
		opt.Session = append(opt.Session, registry.NewDockerAuthProviderWithDepotAuth())
		opt.CacheFrom = filterGHACaches(opt.CacheFrom, "cache-from")
		opt.CacheTo = filterGHACaches(opt.CacheTo, "cache-to")
		opts[name] = opt
	}

	targetProjects, err := readTargetProjects(ctx, files, bakeTargets.Targets, defaults)
	if err != nil {
		return nil, nil, err
	}
	for name, target := range composeTargets {
		if target.ProjectID != "" {
			targetProjects[name] = target.ProjectID
		}
	}

	projects, err := newProjectBuildOptions(options.project, opts, targetProjects)
	return projects, requestedTargets, err
}

type bakeTargets struct {
	CmdContext string
	FileURL    string
	Targets    []string
}

func parseBakeTargets(targets []string) (bkt bakeTargets) {
	bkt.CmdContext = "cwd://"

	if len(targets) > 0 && urlutil.IsRemoteURL(targets[0]) {
		bkt.FileURL = targets[0]
		targets = targets[1:]
		if len(targets) > 0 && urlutil.IsRemoteURL(targets[0]) {
			bkt.CmdContext = targets[0]
			targets = targets[1:]
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
	if progressMode == string(progressui.QuietMode) || os.Getenv("DEPOT_NO_SUMMARY_LINK") != "" || os.Getenv("DEPOT_IN_AUTOMATION") != "" {
		return
	}

	fmt.Fprintln(os.Stderr)
	saved := "target"
	if len(requestedTargets) > 1 {
		saved += "s"
	}

	targetUsage := "--target <TARGET> "
	if len(requestedTargets) == 0 {
		targetUsage = ""
	}

	fmt.Fprintf(os.Stderr, "Saved %s: %s\n", saved, strings.Join(requestedTargets, ","))
	fmt.Fprintf(os.Stderr, "\tTo pull: depot pull --project %s %s\n", project, buildID)

	if len(additionalTags) > 1 {
		fmt.Fprintf(os.Stderr, "\tTo pull save-tags:\n")
		fmt.Fprintf(os.Stderr, "\t\tdocker login registry.depot.dev -u x-token -p $(depot pull-token --project %s)\n", project)
		fmt.Fprintln(os.Stderr)

		// The API returns the same tag for each target.
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
