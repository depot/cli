package sandbox

import (
	"fmt"
	"strings"

	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/pty"
	"github.com/spf13/cobra"
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

			orgID, err := cmd.Flags().GetString("org")
			cobra.CheckErr(err)

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

			return pty.Run(ctx, pty.SessionOptions{
				Token:     token,
				OrgID:     orgID,
				SandboxID: sandboxID,
				SessionID: sessionID,
				Cwd:       cwd,
				Env:       envMap,
			})
		},
	}

	cmd.Flags().String("sandbox-id", "", "ID of the compute to execute the command against")
	cmd.Flags().String("session-id", "", "The session the compute belongs to")
	cmd.Flags().String("cwd", "", "Workdir within the compute. If not provided or invalid, home directory is used.")
	cmd.Flags().StringArray("env", nil, "Environment variables to set (KEY=VALUE), can be specified multiple times.")

	return cmd
}
