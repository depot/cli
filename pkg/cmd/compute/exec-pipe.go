package compute

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

func newComputeExecPipe() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec-pipe [flags]",
		Short: "Execute a command within the compute, then stream bytes to stdin",
		Long:  "Execute a command within the compute, then stream bytes to stdin",
		Example: `
  # Pipe text into a file in the compute
  echo "Hello Depot" | depot compute exec-pipe --sandbox-id 1234567890 --session-id 1234567890 -- /bin/bash -lc "tee /tmp/hello.txt"

  # Pipe a tarball into the compute
  tar czf - ./src | depot compute exec-pipe --sandbox-id 1234567890 --session-id 1234567890 -- /bin/bash -lc "tar xzf - -C /workspace"
`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			token, err := cmd.Flags().GetString("token")
			cobra.CheckErr(err)

			token, err = helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return fmt.Errorf("failed to resolve token: %w", err)
			}

			sandboxID, err := cmd.Flags().GetString("sandbox-id")
			cobra.CheckErr(err)

			if sandboxID == "" {
				return fmt.Errorf("sandbox-id is required")
			}

			sessionID, err := cmd.Flags().GetString("session-id")
			cobra.CheckErr(err)

			if sessionID == "" {
				return fmt.Errorf("session-id is required")
			}

			timeout, err := cmd.Flags().GetInt("timeout")
			cobra.CheckErr(err)

			client := api.NewComputeClient()
			stream := client.ExecPipe(ctx)
			stream.RequestHeader().Set("Authorization", "Bearer "+token)

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
				switch v := resp.Message.(type) {
				case *civ1.ExecuteCommandResponse_Stdout:
					fmt.Fprint(os.Stdout, v.Stdout)
				case *civ1.ExecuteCommandResponse_Stderr:
					fmt.Fprint(os.Stderr, v.Stderr)
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

	return cmd
}
