package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/depot/cli/pkg/api"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/sandbox"
	"github.com/spf13/cobra"
)

func newSandboxExecPipe() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec-pipe [flags]",
		Short: "Execute a command within the compute, then stream bytes to stdin",
		Long:  "Execute a command within the compute, then stream bytes to stdin",
		Example: `
  # Pipe text into a file in the compute
  echo "Hello Depot" | depot sandbox exec-pipe --sandbox-id 1234567890 --session-id 1234567890 -- /bin/bash -lc "tee /tmp/hello.txt"

  # Pipe a tarball into the compute
  tar czf - ./src | depot sandbox exec-pipe --sandbox-id 1234567890 --session-id 1234567890 -- /bin/bash -lc "tar xzf - -C /workspace"
`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			token, orgID, err := resolveAuthAndOrg(ctx, cmd)
			if err != nil {
				return err
			}

			sandboxID, _ := cmd.Flags().GetString("sandbox-id")
			if sandboxID == "" {
				return fmt.Errorf("sandbox-id is required")
			}
			sessionID, _ := cmd.Flags().GetString("session-id")
			if sessionID == "" {
				return fmt.Errorf("session-id is required")
			}

			timeout, _ := cmd.Flags().GetInt("timeout")

			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			client := api.NewComputeClient()

			if err := runHookStage(ctx, cmd, client, token, orgID, sandboxID, sessionID, "on.exec",
				func(h sandbox.HooksSpec) []sandbox.HookSpec { return h.Exec }, os.Stdout, os.Stderr); err != nil {
				return err
			}

			stream := client.ExecPipe(ctx)
			stream.RequestHeader().Set("Authorization", "Bearer "+token)
			if orgID != "" {
				stream.RequestHeader().Set("x-depot-org", orgID)
			}

			// Send init message with the command.
			if err := stream.Send(&civ1.ExecuteCommandPipeRequest{
				Message: &civ1.ExecuteCommandPipeRequest_Init{
					Init: &civ1.ExecuteCommandRequest{
						SandboxId: sandboxID,
						SessionId: sessionID,
						Command: &civ1.Command{
							CommandArray: args,
							TimeoutMs:    int32(timeout),
						},
					},
				},
			}); err != nil {
				return fmt.Errorf("send init: %w", err)
			}

			// Forward stdin to the stream in a goroutine.
			go func() {
				buf := make([]byte, 4096) //nolint:mnd
				for {
					select {
					case <-ctx.Done():
						return
					default:
					}
					n, err := os.Stdin.Read(buf)
					if n > 0 {
						data := make([]byte, n)
						copy(data, buf[:n])
						if sendErr := stream.Send(&civ1.ExecuteCommandPipeRequest{
							Message: &civ1.ExecuteCommandPipeRequest_Stdin{Stdin: data},
						}); sendErr != nil {
							return
						}
					}
					if err != nil {
						_ = stream.CloseRequest()
						return
					}
				}
			}()

			// Read responses from the stream.
			for {
				resp, err := stream.Receive()
				if err != nil {
					if errors.Is(err, io.EOF) {
						return nil
					}
					return fmt.Errorf("stream error: %w", err)
				}
				if len(resp.StdoutRaw) > 0 {
					_, _ = os.Stdout.Write(resp.StdoutRaw)
				}
				if len(resp.StderrRaw) > 0 {
					_, _ = os.Stderr.Write(resp.StderrRaw)
				}
				switch v := resp.Message.(type) {
				case *civ1.ExecuteCommandResponse_Stdout:
					if len(resp.StdoutRaw) == 0 {
						fmt.Fprintln(os.Stdout, v.Stdout)
					}
				case *civ1.ExecuteCommandResponse_Stderr:
					if len(resp.StderrRaw) == 0 {
						fmt.Fprintln(os.Stderr, v.Stderr)
					}
				case *civ1.ExecuteCommandResponse_ExitCode:
					if v.ExitCode != 0 {
						os.Exit(int(v.ExitCode))
					}
					return nil
				}
			}
		},
	}

	cmd.Flags().String("sandbox-id", "", "ID of the compute to execute the command against")
	cmd.Flags().String("session-id", "", "The session the compute belongs to")
	cmd.Flags().Int("timeout", 0, "The execution timeout in milliseconds")
	addHookFlags(cmd, "on.exec")

	return cmd
}
