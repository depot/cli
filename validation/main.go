package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
)

// errScenariosFailed reports that a scenario has the status FAIL or DIFFER.
var errScenariosFailed = errors.New("scenarios failed")

func main() {
	err := run()
	switch {
	case errors.Is(err, errScenariosFailed):
		os.Exit(1)
	case err != nil:
		fmt.Fprintln(os.Stderr, "validation:", err)
		os.Exit(2)
	}
}

func run() error {
	var (
		baseline      = flag.String("baseline", "main", `git revision for the baseline CLI, or "none" to check expectations only`)
		buildkitImage = flag.String("buildkit-image", "depot-validation/buildkitd:private", "buildkitd image that serves the builds")
		filter        = flag.String("run", "", "regular expression that selects scenarios by name")
		parallel      = flag.Int("parallel", 4, "number of scenarios to run at the same time")
		workDir       = flag.String("work", filepath.Join(os.TempDir(), "depot-validation"), "directory for binaries, worktrees, and run files")
		keep          = flag.Bool("keep", false, "keep run directories after each scenario")
		verbose       = flag.Bool("verbose", false, "print differences in full")
		list          = flag.Bool("list", false, "list scenarios and exit")
		reportPath    = flag.String("report", "", "write the full JSON report to this file")
		count         = flag.Int("count", 1, "run each selected scenario this many times")
		extraEnv      environmentFlag
		latency       = flag.Duration("latency", 0, "delay every packet in the network namespace of the builders by this duration")
		upstream      = flag.Bool("upstream", false, "use upstream buildkit "+upstreamBuildkitImage+" and skip the scenarios that need the Depot buildkit exporter")
		hold          = flag.Bool("hold", false, "keep the containers and the API running after the scenarios until interrupted")
	)
	flag.Var(&extraEnv, "env", "set an environment variable for every run, as KEY=VALUE (repeatable)")
	flag.Parse()

	selected, err := selectScenarios(*filter)
	if err != nil {
		return err
	}
	if *upstream {
		buildkitFlagSet := false
		flag.Visit(func(f *flag.Flag) { buildkitFlagSet = buildkitFlagSet || f.Name == "buildkit-image" })
		if !buildkitFlagSet {
			*buildkitImage = upstreamBuildkitImage
		}
		// The TLS server of upstream buildkit v0.13 does not negotiate ALPN,
		// which gRPC clients require by default.
		extraEnv = append(extraEnv, "GRPC_ENFORCE_ALPN_ENABLED=false")
		selected = slices.DeleteFunc(selected, requiresDepotBuildkit)
	}
	selected = repeatScenarios(selected, *count)
	for i := range selected {
		for _, kv := range extraEnv {
			key, value, _ := strings.Cut(kv, "=")
			selected[i] = withEnv(selected[i], key, value)
		}
	}
	if *list {
		for _, s := range selected {
			fmt.Printf("%-40s %s\n", s.Name, s.Description)
		}
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(*workDir, 0o755); err != nil {
		return err
	}
	root, err := repositoryRoot(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "building candidate CLI")
	candidate, err := prepareBinary(ctx, "candidate", root, *workDir, "worktree")
	if err != nil {
		return err
	}
	var base *binary
	if *baseline != "none" {
		source, commit, err := checkoutRevision(ctx, root, *workDir, *baseline)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "building baseline CLI from %s (%s)\n", *baseline, commit[:12])
		base, err = prepareBinary(ctx, "baseline", source, *workDir, commit[:12])
		if err != nil {
			return err
		}
	}

	rc := &runContext{workRoot: *workDir, fixtures: filepath.Join(root, "validation", "fixtures"), keepFiles: *keep}
	if _, err := dockerCommand(ctx, "image", "inspect", *buildkitImage); err != nil {
		if *buildkitImage != upstreamBuildkitImage {
			return fmt.Errorf("buildkitd image %s is not available; build it as described in validation/README.md or pass -upstream", *buildkitImage)
		}
		if _, err := dockerCommand(ctx, "pull", "-q", *buildkitImage); err != nil {
			return err
		}
	}
	for _, image := range []string{runnerImage, "registry:2", "busybox:1.36"} {
		if _, err := dockerCommand(ctx, "image", "inspect", image); err != nil {
			if _, err := dockerCommand(ctx, "pull", "-q", image); err != nil {
				return err
			}
		}
	}
	fmt.Fprintln(os.Stderr, "starting buildkitd and registry containers")
	rc.env, err = startEnvironment(ctx, *buildkitImage, *workDir)
	if err != nil {
		return err
	}
	defer removeContainers(context.Background())
	if *latency > 0 {
		if err := addLatency(ctx, *latency); err != nil {
			return err
		}
	}
	rc.api, err = startFakeAPI(rc.env)
	if err != nil {
		return err
	}
	defer rc.api.close()

	results := make([]scenarioResult, len(selected))
	run := func(i int) {
		results[i] = runScenario(ctx, rc, selected[i], base, candidate, *count > 1 && i%2 == 1)
		fmt.Fprintf(os.Stderr, "%-28s %s\n", results[i].Status, selected[i].Name)
	}
	sem := make(chan struct{}, max(*parallel, 1))
	var wg sync.WaitGroup
	for i, s := range selected {
		if s.Exclusive {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			run(i)
		}()
	}
	wg.Wait()
	for i, s := range selected {
		if s.Exclusive {
			run(i)
		}
	}

	fmt.Println()
	writeSummary(os.Stdout, results, *verbose)
	if *hold {
		fmt.Fprintf(os.Stderr, "\nholding the environment; API at %s; press Ctrl-C to stop\n", rc.api.address)
		<-ctx.Done()
	}
	if *reportPath != "" {
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*reportPath, data, 0o644); err != nil {
			return err
		}
	}
	for _, r := range results {
		if r.Status == statusFail || r.Status == statusDiffer {
			return errScenariosFailed
		}
	}
	return nil
}

// runScenario runs the baseline and then the candidate. When candidateFirst
// is set the order is reversed, so that repeated runs do not favor the
// second binary, which finds warmer caches.
func runScenario(ctx context.Context, rc *runContext, s scenario, base, candidate *binary, candidateFirst bool) scenarioResult {
	var (
		baseObs, candObs *observation
		err              error
	)
	runBase := func() error {
		if base == nil {
			return nil
		}
		baseObs, err = rc.execute(ctx, s, base)
		if err != nil {
			return fmt.Errorf("baseline run: %w", err)
		}
		return nil
	}
	runCandidate := func() error {
		candObs, err = rc.execute(ctx, s, candidate)
		if err != nil {
			return fmt.Errorf("candidate run: %w", err)
		}
		return nil
	}
	order := []func() error{runBase, runCandidate}
	if candidateFirst {
		order = []func() error{runCandidate, runBase}
	}
	for _, run := range order {
		if err := run(); err != nil {
			return scenarioResult{Name: s.Name, Status: statusFail, Failures: []string{err.Error()}}
		}
	}
	return evaluate(s, baseObs, candObs)
}

func selectScenarios(filter string) ([]scenario, error) {
	all := allScenarios()
	seen := map[string]struct{}{}
	for _, s := range all {
		if _, ok := seen[s.Name]; ok {
			return nil, fmt.Errorf("duplicate scenario name %q", s.Name)
		}
		seen[s.Name] = struct{}{}
	}
	if filter == "" {
		return all, nil
	}
	re, err := regexp.Compile(filter)
	if err != nil {
		return nil, err
	}
	var out []scenario
	for _, s := range all {
		if re.MatchString(s.Name) {
			out = append(out, s)
		}
	}
	return out, nil
}

// repeatScenarios runs each scenario count times, to find results that
// change from run to run.
func repeatScenarios(scenarios []scenario, count int) []scenario {
	if count <= 1 {
		return scenarios
	}
	var out []scenario
	for _, s := range scenarios {
		for i := 1; i <= count; i++ {
			r := s
			r.Name = fmt.Sprintf("%s#%d", s.Name, i)
			out = append(out, r)
		}
	}
	return out
}

type environmentFlag []string

func (e *environmentFlag) String() string { return strings.Join(*e, ",") }

func (e *environmentFlag) Set(v string) error {
	*e = append(*e, v)
	return nil
}

const upstreamBuildkitImage = "moby/buildkit:v0.13.2"

// requiresDepotBuildkit reports whether a scenario needs the image data that
// only the Depot buildkit exporter returns: fast --load through the registry
// proxy, and --sbom-dir.
func requiresDepotBuildkit(s scenario) bool {
	fastLoad := slices.Contains(s.Args, "--load") && !s.API.LoadUsingRegistry
	return fastLoad || slices.Contains(s.Args, "--sbom-dir")
}
