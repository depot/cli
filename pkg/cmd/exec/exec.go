package exec

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/depot/cli/pkg/connection"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/machine"
	"github.com/depot/cli/pkg/progresshelper"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli/command"
	"github.com/spf13/cobra"
)

func NewCmdExec(dockerCli command.Cli) *cobra.Command {
	var (
		envVar       string
		token        string
		projectID    string
		platform     string
		progressMode string
	)

	run := func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		token, err := helpers.ResolveToken(ctx, token)
		if err != nil {
			return err
		}
		projectID = helpers.ResolveProjectID(projectID)
		if projectID == "" {
			selectedProject, err := helpers.OnboardProject(ctx, token)
			if err != nil {
				return err
			}
			projectID = selectedProject.ID
		}

		if token == "" {
			return fmt.Errorf("missing API token, please run `depot login`")
		}

		platform, err = ResolveMachinePlatform(platform)
		if err != nil {
			return err
		}

		req := &cliv1.CreateBuildRequest{
			ProjectId: &projectID,
			Options:   []*cliv1.BuildOptions{{Command: cliv1.Command_COMMAND_EXEC}},
		}

		if len(args) > 0 && args[0] == "dagger" {
			daggerVersion, _ := helpers.ResolveDaggerVersion()
			if daggerVersion != "" {
				req = helpers.NewDaggerRequest(projectID, daggerVersion)
			}
		}

		build, err := helpers.BeginBuild(ctx, req, token)
		if err != nil {
			return fmt.Errorf("unable to begin build: %w", err)
		}

		var buildErr error
		defer func() {
			build.Finish(buildErr)
		}()

		printCtx, cancel := context.WithCancel(ctx)
		printer, buildErr := progress.NewPrinter(printCtx, os.Stderr, os.Stderr, progressMode)
		if buildErr != nil {
			cancel()
			return buildErr
		}

		reportingWriter := progresshelper.NewReporter(printCtx, printer, build.ID, build.Token)

		var builder *machine.Machine
		buildErr = progresshelper.WithLog(reportingWriter, fmt.Sprintf("[depot] launching %s machine", platform), func() error {
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
			reportingWriter.Close()
			return buildErr
		}

		defer func() { _ = builder.Release() }()

		// Wait for connection to be ready.
		var conn net.Conn
		buildErr = progresshelper.WithLog(reportingWriter, fmt.Sprintf("[depot] connecting to %s machine", platform), func() error {
			conn, buildErr = connection.TLSConn(ctx, builder)
			if buildErr != nil {
				return fmt.Errorf("unable to connect: %w", buildErr)
			}
			_ = conn.Close()
			return nil
		})
		cancel()
		reportingWriter.Close()

		listener, localAddr, buildErr := connection.LocalListener()
		if buildErr != nil {
			return buildErr
		}
		proxy := connection.NewProxy(listener, builder)

		proxyCtx, proxyCancel := context.WithCancel(ctx)
		defer proxyCancel()
		go func() { _ = proxy.Start(proxyCtx) }()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan)

		subCmd := exec.CommandContext(ctx, args[0], args[1:]...)

		env := os.Environ()
		subCmd.Env = append(env, fmt.Sprintf("%s=%s", envVar, localAddr))
		subCmd.Stdin = os.Stdin
		subCmd.Stdout = os.Stdout
		subCmd.Stderr = os.Stderr

		buildErr = subCmd.Start()
		if buildErr != nil {
			return buildErr
		}

		go func() {
			for {
				sig := <-sigChan
				_ = subCmd.Process.Signal(sig)
			}
		}()

		buildErr = subCmd.Wait()
		if buildErr != nil {
			return buildErr
		}

		return nil
	}

	cmd := &cobra.Command{
		Hidden: true,
		Use:    "exec [flags] command [args...]",
		Short:  "Execute a command with injected BuildKit connection",
		Args:   cli.RequiresMinArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if err := run(cmd, args); err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
						os.Exit(status.ExitStatus())
					}
				}

				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		},
	}

	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVar(&envVar, "env-var", "BUILDKIT_HOST", "Environment variable name for the BuildKit connection")
	cmd.Flags().StringVar(&platform, "platform", "", "Platform to execute the command on")
	cmd.Flags().StringVar(&projectID, "project", "", "Depot project ID")
	cmd.Flags().StringVar(&progressMode, "progress", "auto", `Set type of progress output ("auto", "plain", "tty")`)
	cmd.Flags().StringVar(&token, "token", "", "Depot token")

	return cmd
}

func ResolveMachinePlatform(platform string) (string, error) {
	if platform == "" {
		platform = os.Getenv("DEPOT_BUILD_PLATFORM")
	}

	switch platform {
	case "linux/arm64":
		platform = "arm64"
	case "linux/amd64":
		platform = "amd64"
	case "":
		if strings.HasPrefix(runtime.GOARCH, "arm") {
			platform = "arm64"
		} else {
			platform = "amd64"
		}
	default:
		return "", fmt.Errorf("invalid platform: %s (must be one of: linux/amd64, linux/arm64)", platform)
	}

	return platform, nil
}
