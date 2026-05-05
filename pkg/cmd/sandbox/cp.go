package sandbox

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

// newSandboxCp copies files between local fs and a running sandbox by
// streaming a tar archive over the existing exec channel. Same wire path as
// `sandbox exec` / `exec-pipe`, so auth and org handling come for free; same
// model Modal lands on (compose `exec` with `tar`, no scp/SFTP primitive).
func newSandboxCp() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cp <src> <dst>",
		Short: "Copy files between local filesystem and a sandbox",
		Long: `Copy files or directories between the local filesystem and a running sandbox.

Either source or destination must be of the form <sandbox-id>:<path>; the
other side is interpreted as a local path. The transfer streams a tar archive
through the same exec channel as 'sandbox exec', so semantics match GNU tar
(preserves modes, symlinks, and timestamps; recursive by default).

The sandbox needs a 'tar' binary on PATH, which every Linux base image has.`,
		Example: `
  # local → sandbox
  depot sandbox cp ./report.md cs-abc123:/workspace/

  # sandbox → local
  depot sandbox cp cs-abc123:/workspace/report.md ./

  # whole directory in either direction
  depot sandbox cp ./agents cs-abc123:/workspace/
  depot sandbox cp cs-abc123:/output ./local-output
`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			src, dst := args[0], args[1]
			srcID, srcPath, srcRemote := splitCpSpec(src)
			dstID, dstPath, dstRemote := splitCpSpec(dst)

			if srcRemote && dstRemote {
				return fmt.Errorf("cp: cannot copy sandbox→sandbox; one side must be a local path")
			}
			if !srcRemote && !dstRemote {
				return fmt.Errorf("cp: at least one of <src>/<dst> must be of the form <sandbox-id>:<path>")
			}

			token, _ := cmd.Flags().GetString("token")
			token, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return fmt.Errorf("resolve token: %w", err)
			}
			orgID, _ := cmd.Flags().GetString("org")
			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			sandboxID := dstID
			if srcRemote {
				sandboxID = srcID
			}

			sbClient := api.NewSandboxClient()
			res, err := sbClient.GetSandbox(ctx, api.WithAuthenticationAndOrg(
				connect.NewRequest(&agentv1.GetSandboxRequest{SandboxId: sandboxID}), token, orgID))
			if err != nil {
				return fmt.Errorf("get sandbox: %w", err)
			}
			if res.Msg.Sandbox == nil {
				return fmt.Errorf("sandbox %s not found", sandboxID)
			}
			sessionID := res.Msg.Sandbox.SessionId

			if srcRemote {
				return cpRemoteToLocal(ctx, token, orgID, sandboxID, sessionID, srcPath, dstPath, cmd.ErrOrStderr())
			}
			return cpLocalToRemote(ctx, token, orgID, sandboxID, sessionID, srcPath, dstPath, cmd.ErrOrStderr())
		},
	}
	return cmd
}

// splitCpSpec parses "<id>:<path>" or "<path>". Returns (sandboxID, path, remote).
// Anything that looks unambiguously local — starts with /, ./, ../, ., .. — is
// treated as local even if it contains a colon. Bare "id:path" with no path
// hint counts as remote.
func splitCpSpec(s string) (string, string, bool) {
	if s == "" {
		return "", "", false
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || s == "." || s == ".." {
		return "", s, false
	}
	idx := strings.IndexByte(s, ':')
	if idx <= 0 {
		return "", s, false
	}
	return s[:idx], s[idx+1:], true
}

// cpLocalToRemote streams `tar c` from disk into the sandbox's `tar x`.
// Local tar runs in the dirname of src so the archive carries just the
// basename — `tar x -C <dst>` then drops it under <dst>/<basename>, matching
// docker cp semantics.
func cpLocalToRemote(ctx context.Context, token, orgID, sandboxID, sessionID, src, dst string, stderr io.Writer) error {
	if dst == "" {
		dst = "."
	}
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("abs src: %w", err)
	}
	if _, err := os.Stat(absSrc); err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	srcDir := filepath.Dir(absSrc)
	srcBase := filepath.Base(absSrc)

	tarCmd := exec.CommandContext(ctx, "tar", "c", "-C", srcDir, "-f", "-", srcBase)
	tarCmd.Stderr = stderr
	tarStdout, err := tarCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("local tar stdout pipe: %w", err)
	}
	if err := tarCmd.Start(); err != nil {
		return fmt.Errorf("start local tar: %w", err)
	}

	client := api.NewComputeClient()
	stream := client.ExecPipe(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+token)
	if orgID != "" {
		stream.RequestHeader().Set("x-depot-org", orgID)
	}

	if err := stream.Send(&civ1.ExecuteCommandPipeRequest{
		Message: &civ1.ExecuteCommandPipeRequest_Init{
			Init: &civ1.ExecuteCommandRequest{
				SandboxId: sandboxID,
				SessionId: sessionID,
				// /bin/sh wrapper handles PATH resolution (the proxy
				// won't find a bare "tar"); $1 is the dst dir, passed
				// as an argv positional so we don't hand-quote.
				Command: &civ1.Command{
					CommandArray: []string{"/bin/sh", "-c", `exec tar x -C "$1" -f -`, "depot-cp", dst},
				},
			},
		},
	}); err != nil {
		_ = tarCmd.Process.Kill()
		_ = tarCmd.Wait()
		return fmt.Errorf("send init: %w", err)
	}

	// Forward local tar's stdout into the stream.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := tarStdout.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				if sendErr := stream.Send(&civ1.ExecuteCommandPipeRequest{
					Message: &civ1.ExecuteCommandPipeRequest_Stdin{Stdin: data},
				}); sendErr != nil {
					_ = stream.CloseRequest()
					return
				}
			}
			if readErr != nil {
				_ = stream.CloseRequest()
				return
			}
		}
	}()

	for {
		resp, err := stream.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			_ = tarCmd.Process.Kill()
			_ = tarCmd.Wait()
			return fmt.Errorf("stream: %w", err)
		}
		switch v := resp.Message.(type) {
		case *civ1.ExecuteCommandResponse_Stderr:
			fmt.Fprint(stderr, v.Stderr)
		case *civ1.ExecuteCommandResponse_ExitCode:
			waitErr := tarCmd.Wait()
			if v.ExitCode != 0 {
				return fmt.Errorf("remote tar x exited %d", v.ExitCode)
			}
			if waitErr != nil {
				return fmt.Errorf("local tar c: %w", waitErr)
			}
			return nil
		}
	}
	return tarCmd.Wait()
}

// cpRemoteToLocal streams remote `tar c | base64` → local `base64 -d | tar x`.
//
// Why the base64 detour: ExecuteCommandResponse.stdout is a proto `string`
// field (api.proto:74), which corrupts non-UTF-8 bytes — tar archives are
// binary, so a raw stream loses newlines and trailing nulls in transit.
// Wrapping in base64 keeps the response ASCII; we decode locally in Go
// before piping into tar.
//
// (Push is fine without this trick: ExecuteCommandPipeRequest.stdin is
// `bytes`, so binary survives.)
//
// Drop this whole encoding hop once DEP-4404 ships a `bytes stdout_raw`
// alongside the legacy string field.
func cpRemoteToLocal(ctx context.Context, token, orgID, sandboxID, sessionID, src, dst string, stderr io.Writer) error {
	if dst == "" {
		dst = "."
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("mkdir dst: %w", err)
	}
	srcDir := path.Dir(src)
	srcBase := path.Base(src)

	tarCmd := exec.CommandContext(ctx, "tar", "x", "-C", dst, "-f", "-")
	tarCmd.Stderr = stderr
	tarStdin, err := tarCmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("local tar stdin pipe: %w", err)
	}
	if err := tarCmd.Start(); err != nil {
		return fmt.Errorf("start local tar: %w", err)
	}

	// Pipe between base64-decoded stream output and local tar's stdin.
	// base64.NewDecoder accepts wrapped base64 (ignores newlines), so we
	// don't care whether the remote `base64` wraps at 76 or not.
	pr, pw := io.Pipe()
	decoder := base64.NewDecoder(base64.StdEncoding, pr)
	decodeDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(tarStdin, decoder)
		_ = tarStdin.Close()
		decodeDone <- err
	}()

	client := api.NewComputeClient()
	stream, err := client.RemoteExec(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(&civ1.ExecuteCommandRequest{
		SandboxId: sandboxID,
		SessionId: sessionID,
		Command: &civ1.Command{
			CommandArray: []string{"/bin/sh", "-c", `exec tar c -C "$1" -f - "$2" | base64`, "depot-cp", srcDir, srcBase},
		},
	}), token, orgID))
	if err != nil {
		_ = pw.Close()
		<-decodeDone
		_ = tarCmd.Process.Kill()
		_ = tarCmd.Wait()
		return fmt.Errorf("exec: %w", err)
	}

	for stream.Receive() {
		switch v := stream.Msg().Message.(type) {
		case *civ1.ExecuteCommandResponse_Stdout:
			if _, werr := io.WriteString(pw, v.Stdout); werr != nil {
				_ = pw.CloseWithError(werr)
				<-decodeDone
				_ = tarCmd.Process.Kill()
				_ = tarCmd.Wait()
				return fmt.Errorf("pipe write: %w", werr)
			}
		case *civ1.ExecuteCommandResponse_Stderr:
			fmt.Fprint(stderr, v.Stderr)
		case *civ1.ExecuteCommandResponse_ExitCode:
			_ = pw.Close()
			decodeErr := <-decodeDone
			waitErr := tarCmd.Wait()
			if v.ExitCode != 0 {
				return fmt.Errorf("remote tar c | base64 exited %d", v.ExitCode)
			}
			if decodeErr != nil {
				return fmt.Errorf("base64 decode: %w", decodeErr)
			}
			if waitErr != nil {
				return fmt.Errorf("local tar x: %w", waitErr)
			}
			return nil
		}
	}
	_ = pw.Close()
	<-decodeDone
	if err := stream.Err(); err != nil {
		_ = tarCmd.Process.Kill()
		_ = tarCmd.Wait()
		return fmt.Errorf("stream: %w", err)
	}
	return tarCmd.Wait()
}
