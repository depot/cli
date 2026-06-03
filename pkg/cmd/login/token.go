package login

import (
	"fmt"

	"github.com/depot/cli/pkg/config"
	"github.com/spf13/cobra"
)

// newCmdLoginToken builds `depot login token`, which prints the stored Depot
// API token to stdout so it can be captured in shells, e.g. `$(depot login
// token)`. When no token is stored it returns an error (written to stderr by
// cobra) and prints nothing to stdout, keeping command substitution clean.
func newCmdLoginToken() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print the stored Depot API token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			token := config.GetApiToken()
			if token == "" {
				return fmt.Errorf("not logged in: no Depot API token found, please run `depot login`")
			}

			fmt.Fprintln(cmd.OutOrStdout(), token)
			return nil
		},
	}

	return cmd
}
