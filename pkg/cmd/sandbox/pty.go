package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newSandboxPty() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pty [flags]",
		Short: "Open a pseudo-terminal within the compute",
		Long:  "Open a pseudo-terminal within the compute",
		Example: `
  # open a pseudo-terminal within the compute
  depot sandbox pty --sandbox-id 1234567890 --session-id 1234567890

  # set terminal workdir
  depot sandbox pty --sandbox-id 1234567890 --session-id 1234567890 --cwd /tmp

  # set terminal environment variables
  depot sandbox pty --sandbox-id 1234567890 --session-id 1234567890 --env FOO=BAR --env BAR=FOO
`,
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

			cwd, err := cmd.Flags().GetString("cwd")
			cobra.CheckErr(err)

			envSlice, err := cmd.Flags().GetStringArray("env")
			cobra.CheckErr(err)

			envMap := make(map[string]string, len(envSlice))
			for _, e := range envSlice {
				k, v, ok := strings.Cut(e, "=")
				if !ok {
					return fmt.Errorf("invalid env format %q, expected KEY=VALUE", e)
				}
				envMap[k] = v
			}

			// Get current terminal size.
			fd := int(os.Stdin.Fd())
			rows, cols := 24, 80 //nolint:mnd
			if w, h, err := term.GetSize(fd); err == nil {
				cols, rows = w, h
			}

			// Put terminal in raw mode.
			oldState, err := term.MakeRaw(fd)
			if err != nil {
				return fmt.Errorf("make raw: %w", err)
			}
			defer term.Restore(fd, oldState) //nolint:errcheck

			// Restore terminal on signals that would otherwise leave it raw.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				term.Restore(fd, oldState) //nolint:errcheck
				os.Exit(1)
			}()
			defer signal.Stop(sigCh)

			client := api.NewComputeClient()
			stream := client.OpenPtySession(ctx)
			stream.RequestHeader().Set("Authorization", "Bearer "+token)

			// Send init message.
			if err := stream.Send(&civ1.OpenPtySessionRequest{
				Message: &civ1.OpenPtySessionRequest_Init{
					Init: &civ1.PtySession{
						SandboxId: sandboxID,
						SessionId: sessionID,
						Cwd:       cwd,
						Env:       envMap,
						Rows:      uint32(rows),
						Cols:      uint32(cols),
					},
				},
			}); err != nil {
				return fmt.Errorf("send pty init: %w", err)
			}

			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			sendCh := make(chan *civ1.OpenPtySessionRequest, 1)

			go func() {
				for {
					select {
					case msg := <-sendCh:
						_ = stream.Send(msg)
					case <-ctx.Done():
						_ = stream.CloseRequest()
						return
					}
				}
			}()

			// Watch for terminal resize events (no-op on Windows).
			stopResize := watchTerminalResize(ctx, fd, sendCh)
			defer stopResize()

			// Forward stdin to the stream.
			go func() {
				buf := make([]byte, 4096) //nolint:mnd
				for {
					n, err := os.Stdin.Read(buf)
					if n > 0 {
						data := make([]byte, n)
						copy(data, buf[:n])
						select {
						case sendCh <- &civ1.OpenPtySessionRequest{
							Message: &civ1.OpenPtySessionRequest_Stdin{Stdin: data},
						}:
						case <-ctx.Done():
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()

			// Read stdout and exit code from the stream.
			for {
				resp, err := stream.Receive()
				if err != nil {
					if errors.Is(err, io.EOF) {
						return nil
					}
					return fmt.Errorf("recv: %w", err)
				}
				switch m := resp.GetMessage().(type) {
				case *civ1.OpenPtySessionResponse_Stdout:
					os.Stdout.Write(m.Stdout) //nolint:errcheck
				case *civ1.OpenPtySessionResponse_ExitCode:
					fmt.Fprintf(os.Stderr, "\r\n[exit %d]\r\n", m.ExitCode)
					if m.ExitCode != 0 {
						term.Restore(fd, oldState) //nolint:errcheck
						os.Exit(int(m.ExitCode))
					}
					return nil
				}
			}
		},
	}

	cmd.Flags().String("sandbox-id", "", "ID of the compute to execute the command against")
	cmd.Flags().String("session-id", "", "The session the compute belongs to")
	cmd.Flags().String("cwd", "", "Workdir within the compute. If not provided or invalid, home directory is used.")
	cmd.Flags().StringArray("env", nil, "Environment variables to set (KEY=VALUE), can be specified multiple times.")

	return cmd
}
