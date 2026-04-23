package ci

import (
	"fmt"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

func NewCmdRunShow() *cobra.Command {
	var (
		orgID  string
		token  string
		output string
	)

	cmd := &cobra.Command{
		Use:     "show <run-id>",
		Aliases: []string{"get"},
		Short:   "Show a CI run",
		Long:    `Show a flat CI run record.`,
		Example: `  depot ci run show <run-id>
  depot ci run get <run-id>
  depot ci run show <run-id> --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			runID := args[0]

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

			resp, err := api.CIGetRun(ctx, tokenVal, orgID, runID)
			if err != nil {
				return fmt.Errorf("failed to get run: %w", err)
			}

			if output == "json" {
				return writeJSON(resp)
			}

			printRun(resp)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")

	return cmd
}

func printRun(run *civ1.GetRunResponse) {
	fmt.Printf("%-10s %s\n", "Org", run.OrgId)
	fmt.Printf("%-10s %s\n", "Run", run.RunId)
	fmt.Printf("%-10s %s\n", "Repo", run.Repo)
	fmt.Printf("%-10s %s\n", "Status", formatStatus(run.Status))
	fmt.Printf("%-10s %s\n", "Trigger", run.Trigger)
	fmt.Printf("%-10s %s\n", "Ref", run.Ref)
	fmt.Printf("%-10s %s\n", "Sha", run.Sha)
	fmt.Printf("%-10s %s\n", "Head sha", run.HeadSha)
	fmt.Printf("%-10s %s\n", "Created", run.CreatedAt)
	fmt.Printf("%-10s %s\n", "Started", run.StartedAt)
	fmt.Printf("%-10s %s\n", "Finished", run.FinishedAt)
}
