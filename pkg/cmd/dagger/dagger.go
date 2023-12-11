package dagger

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/machine"
	"github.com/spf13/cobra"
)

/*
TODO: check context canceling during build.

*/

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
			for _, arg := range args {
				if arg == "--help" || arg == "-h" {
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

	var builder *machine.Machine
	for i := 0; i < 2; i++ {
		builder, buildErr = machine.Acquire(ctx, build.ID, build.Token, platform)
		if buildErr == nil {
			break
		}
	}
	if buildErr != nil {
		return buildErr
	}

	defer func() { _ = builder.Release() }()

	// Wait until able to connect.
	conn, buildErr := machine.TLSConn(ctx, builder)
	if buildErr != nil {
		return fmt.Errorf("unable to connect: %w", buildErr)
	}
	_ = conn.Close()

	listener, localAddr, err := LocalListener()
	if err != nil {
		return err
	}
	proxy := NewProxy(listener, builder)
	// TODO handle error and context canceling.
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
