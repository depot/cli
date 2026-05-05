package sandbox

import (
	"fmt"
	"os"
	"os/exec"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
)

func newSandboxSSH() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh <sandbox-id> [-- ssh-args...]",
		Short: "Open an SSH session to a running sandbox",
		Long: `Resolve the sandbox's ssh endpoint via the API, materialize the
private key into a temp file with 0600 permissions, and exec ssh.

Anything after a literal "--" is forwarded to ssh as additional arguments.`,
		Example: `
  # Interactive shell
  depot sandbox ssh cs-abc123

  # Run a single command
  depot sandbox ssh cs-abc123 -- -t 'tail -f /var/log/agent.log'

  # Use a session id directly (skip the sandbox→session lookup)
  depot sandbox ssh --session-id ses-xyz
`,
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			token, _ := cmd.Flags().GetString("token")
			token, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return fmt.Errorf("resolve token: %w", err)
			}
			orgID, _ := cmd.Flags().GetString("org")
			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			sessionID, _ := cmd.Flags().GetString("session-id")
			var sandboxID string
			if len(args) > 0 {
				sandboxID = args[0]
				args = args[1:]
			}

			client := api.NewSandboxClient()

			if sessionID == "" {
				if sandboxID == "" {
					return fmt.Errorf("provide a sandbox id or --session-id")
				}
				getRes, err := client.GetSandbox(ctx, api.WithAuthenticationAndOrg(
					connect.NewRequest(&agentv1.GetSandboxRequest{SandboxId: sandboxID}), token, orgID))
				if err != nil {
					return fmt.Errorf("get sandbox: %w", err)
				}
				if getRes.Msg.Sandbox == nil {
					return fmt.Errorf("sandbox %s not found", sandboxID)
				}
				sessionID = getRes.Msg.Sandbox.SessionId
			}

			sshRes, err := client.GetSSHConnection(ctx, api.WithAuthenticationAndOrg(
				connect.NewRequest(&agentv1.GetSSHConnectionRequest{SessionId: sessionID}), token, orgID))
			if err != nil {
				return fmt.Errorf("get ssh connection: %w", err)
			}
			conn := sshRes.Msg.SshConnection
			if conn == nil {
				return fmt.Errorf("sandbox has no ssh connection (was ssh.enabled set?)")
			}

			keyName := sandboxID
			if keyName == "" {
				keyName = sessionID
			}
			keyPath, err := writeSandboxSSHKey(keyName, conn.PrivateKey)
			if err != nil {
				return err
			}

			sshArgs := []string{
				"-i", keyPath,
				"-p", fmt.Sprintf("%d", conn.Port),
				"-o", "StrictHostKeyChecking=accept-new",
				"-o", "UserKnownHostsFile=" + keyPath + ".known_hosts",
				fmt.Sprintf("%s@%s", conn.Username, conn.Host),
			}
			sshArgs = append(sshArgs, args...)

			ssh := exec.CommandContext(ctx, "ssh", sshArgs...)
			ssh.Stdin = os.Stdin
			ssh.Stdout = os.Stdout
			ssh.Stderr = os.Stderr
			return ssh.Run()
		},
	}

	cmd.Flags().String("session-id", "", "Skip the sandbox→session lookup and use this session id directly")
	return cmd
}
