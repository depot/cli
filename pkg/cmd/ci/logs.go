package ci

import (
	"fmt"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdLogs() *cobra.Command {
	var (
		orgID string
		token string
	)

	cmd := &cobra.Command{
		Use:   "logs <attempt-id>",
		Short: "Fetch logs for a CI job attempt [beta]",
		Long:  "Fetch and display log output for a CI job attempt.\n\nThis command is in beta and subject to change.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}

			ctx := cmd.Context()
			attemptID := args[0]

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			lines, err := api.CIGetJobAttemptLogs(ctx, tokenVal, orgID, attemptID)
			if err != nil {
				return fmt.Errorf("failed to get job attempt logs: %w", err)
			}

			for _, line := range lines {
				fmt.Println(line.Body)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")

	return cmd
}
