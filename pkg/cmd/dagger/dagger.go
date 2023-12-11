package dagger

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/machine"
	"github.com/depot/cli/pkg/progress"
	buildxprogress "github.com/docker/buildx/util/progress"
	"github.com/spf13/cobra"
)

var (
	daggerPath    string
	daggerVersion string
	projectID     string
	token         string
	platform      string
)

func NewCmdList() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "dagger [command]",
		Short:                 "Run Dagger pipelines in Depot",
		DisableFlagParsing:    true,
		DisableFlagsInUseLine: true,
		DisableSuggestions:    true,
		PreRunE:               CheckDagger,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if args[0] == "--help" || args[0] == "-h" {
					return help()
				}
			}
			return run(cmd.Context(), args)
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Depot project ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot token")
	cmd.Flags().StringVar(&platform, "platform", "", `Run dagger on specific platform ("linux/amd64", "linux/arm64")`)

	return cmd
}

func run(ctx context.Context, args []string) error {
	token, err := helpers.ResolveToken(ctx, token)
	if err != nil {
		return err
	}

	if token == "" {
		return fmt.Errorf("missing token, please run `depot login`")
	}

	var selectedProject *helpers.SelectedProject
	projectID = helpers.ResolveProjectID(projectID)
	if projectID == "" { // No locally saved depot.json.
		selectedProject, err = helpers.OnboardProject(ctx, token)
		if err != nil {
			return err
		}
		projectID = selectedProject.ID
	}

	platform, err = helpers.ResolveMachinePlatform(platform)
	if err != nil {
		return err
	}

	req := helpers.NewDaggerRequest(projectID, daggerVersion)
	build, err := helpers.BeginBuild(ctx, req, token)
	if err != nil {
		return fmt.Errorf("unable to begin build: %w", err)
	}

	var buildErr error
	defer func() {
		build.Finish(buildErr)
	}()

	printCtx, cancel := context.WithCancel(ctx)
	buildxprinter, buildErr := buildxprogress.NewPrinter(printCtx, os.Stderr, os.Stderr, "auto")
	if buildErr != nil {
		cancel()
		return buildErr
	}

	reporter, finishReporter, buildErr := progress.NewProgress(printCtx, build.ID, build.Token, buildxprinter)
	if buildErr != nil {
		cancel()
		return buildErr
	}

	var builder *machine.Machine
	buildErr = reporter.WithLog("[depot] launching amd64 machine", func() error {
		for i := 0; i < 2; i++ {
			builder, buildErr = machine.Acquire(ctx, build.ID, build.Token, platform)
			if buildErr == nil {
				break
			}
		}
		return buildErr
	})
	if buildErr != nil {
		cancel()
		finishReporter()
		return buildErr
	}

	defer func() { _ = builder.Release() }()

	// Wait for connection to be ready.
	var conn net.Conn
	buildErr = reporter.WithLog(fmt.Sprintf("[depot] connecting to %s machine", platform), func() error {
		conn, buildErr = machine.TLSConn(ctx, builder)
		if buildErr != nil {
			return fmt.Errorf("unable to connect: %w", buildErr)
		}
		_ = conn.Close()
		return nil
	})
	cancel()
	finishReporter()

	listener, localAddr, buildErr := LocalListener()
	if buildErr != nil {
		return buildErr
	}
	proxy := NewProxy(listener, builder)

	ctx, proxyCancel := context.WithCancel(ctx)
	defer proxyCancel()
	go func() { _ = proxy.Start(ctx) }()

	cmd := exec.Command(daggerPath, args...)

	env := os.Environ()
	cmd.Env = append(env, fmt.Sprintf("_EXPERIMENTAL_DAGGER_RUNNER_HOST=%s", localAddr))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	buildErr = cmd.Start()
	if buildErr != nil {
		return buildErr
	}

	buildErr = cmd.Wait()
	if buildErr != nil {
		return buildErr
	}

	return nil
}

func help() error {
	output, err := exec.Command(daggerPath, "help").Output()
	if err != nil {
		return err
	}

	help := strings.Replace(string(output), "  dagger", "depot dagger", -1)
	help = strings.Replace(help, "Flags:", "Flags:\n      --project string      Depot project ID\n      --token string        Depot token\n      --platform string     Run builds on this platform (\"dynamic\", \"linux/amd64\", \"linux/arm64\") (default \"dynamic\")\n", -1)
	fmt.Printf("%s\n", help)

	return nil
}

func CheckDagger(_ *cobra.Command, _ []string) error {
	var err error
	daggerPath, err = exec.LookPath("dagger")
	if err != nil {
		return err
	}

	output, err := exec.Command(daggerPath, "version").Output()
	if err != nil {
		return err
	}
	parsed := strings.Split(string(output), " ")
	if len(parsed) < 2 {
		return fmt.Errorf("unable able to parse dagger version")
	}
	daggerVersion = parsed[1]
	return nil
}
