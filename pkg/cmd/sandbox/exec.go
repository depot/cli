package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
)

type execOptions struct {
	noWait bool
	token  string
	orgID  string
	stdout io.Writer
	stderr io.Writer
}

// NewCmdExec creates the sandbox exec subcommand
func NewCmdExec() *cobra.Command {
	opts := &execOptions{
		stdout: os.Stdout,
		stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "exec <sandbox-or-session-id> <command> [args...]",
		Short: "Execute a command in a running sandbox",
		Long: `Execute a command in a running sandbox using the ExecInSandbox API.

Accepts either a sandbox ID or session ID. If a session ID is provided,
the command resolves it to a sandbox ID automatically.`,
		Example: `  # List files in a sandbox by sandbox ID
  depot sandbox exec sbx_abc123 ls -la

  # Run a script using a session ID
  depot sandbox exec sess_abc123 ./build.sh

  # Fire and forget a long-running command
  depot sandbox exec --no-wait sbx_abc123 sleep 3600`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			command := strings.Join(args[1:], " ")
			return runExec(cmd.Context(), id, command, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.noWait, "no-wait", false, "Don't wait for the command to complete (fire and forget)")
	cmd.Flags().StringVar(&opts.token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&opts.orgID, "org", "", "Organization ID")

	return cmd
}

func runExec(ctx context.Context, id, command string, opts *execOptions) error {
	token, err := helpers.ResolveOrgAuth(ctx, opts.token)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}

	if opts.orgID == "" {
		opts.orgID = os.Getenv("DEPOT_ORG_ID")
	}
	if opts.orgID == "" {
		opts.orgID = config.GetCurrentOrganization()
	}

	sandboxClient := api.NewSandboxClient()

	waitForCommand := !opts.noWait

	// Try ExecInSandbox with the given ID as sandbox_id first
	res, err := callExecInSandbox(ctx, sandboxClient, token, opts.orgID, id, command, waitForCommand)
	if err != nil {
		// If not found, the ID might be a session ID — resolve it
		if connect.CodeOf(err) == connect.CodeNotFound {
			sandboxID, resolveErr := resolveSandboxID(ctx, sandboxClient, token, opts.orgID, id)
			if resolveErr != nil {
				return fmt.Errorf("could not find sandbox or session with ID %q: %w", id, resolveErr)
			}
			res, err = callExecInSandbox(ctx, sandboxClient, token, opts.orgID, sandboxID, command, waitForCommand)
			if err != nil {
				return fmt.Errorf("failed to execute command: %w", err)
			}
		} else {
			return fmt.Errorf("failed to execute command: %w", err)
		}
	}

	// If no-wait, we're done
	if opts.noWait || res.Msg.CommandResult == nil {
		return nil
	}

	cr := res.Msg.CommandResult
	if cr.Stdout != "" {
		fmt.Fprint(opts.stdout, cr.Stdout)
	}
	if cr.Stderr != "" {
		fmt.Fprint(opts.stderr, cr.Stderr)
	}
	if cr.ExitCode != 0 {
		os.Exit(int(cr.ExitCode))
	}

	return nil
}

func callExecInSandbox(
	ctx context.Context,
	client interface {
		ExecInSandbox(context.Context, *connect.Request[agentv1.ExecInSandboxRequest]) (*connect.Response[agentv1.ExecInSandboxResponse], error)
	},
	token, orgID, sandboxID, command string,
	waitForCommand bool,
) (*connect.Response[agentv1.ExecInSandboxResponse], error) {
	req := &agentv1.ExecInSandboxRequest{
		SandboxId:      sandboxID,
		Command:        command,
		WaitForCommand: &waitForCommand,
	}
	return client.ExecInSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
}

// resolveSandboxID looks up a sandbox_id by iterating sandboxes and matching session_id.
func resolveSandboxID(ctx context.Context, client interface {
	ListSandboxs(context.Context, *connect.Request[agentv1.ListSandboxsRequest]) (*connect.Response[agentv1.ListSandboxsResponse], error)
}, token, orgID, sessionID string) (string, error) {
	req := &agentv1.ListSandboxsRequest{}
	res, err := client.ListSandboxs(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return "", fmt.Errorf("failed to list sandboxes: %w", err)
	}

	for _, sb := range res.Msg.Sandboxes {
		if sb.SessionId == sessionID {
			return sb.SandboxId, nil
		}
	}

	return "", fmt.Errorf("no sandbox found for session ID %q", sessionID)
}

