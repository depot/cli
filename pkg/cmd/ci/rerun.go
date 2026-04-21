package ci

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdRerun() *cobra.Command {
	var (
		orgID      string
		token      string
		workflowID string
		output     string
	)

	cmd := &cobra.Command{
		Use:   "rerun <run-id>",
		Short: "Re-run a CI workflow [beta]",
		Long: `Re-run every job in a finished workflow. For runs that contain more than one workflow,
--workflow is required to disambiguate.

This command is in beta and subject to change.`,
		Example: `  # Re-run the only workflow in a run
  depot ci rerun <run-id>

  # Re-run a specific workflow
  depot ci rerun <run-id> --workflow <workflow-id>

  # Output the RPC response as JSON
  depot ci rerun <run-id> --workflow <workflow-id> --output json`,
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

			wfID := workflowID
			if wfID == "" {
				wfID, err = resolveSingleWorkflow(ctx, tokenVal, orgID, runID)
				if err != nil {
					return err
				}
			}

			resp, err := api.CIRerunWorkflow(ctx, tokenVal, orgID, wfID)
			if err != nil {
				return fmt.Errorf("failed to rerun workflow: %w", err)
			}

			if output == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}

			fmt.Printf("Rerunning workflow %s (%d jobs reset)\n", resp.WorkflowId, resp.JobCount)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&workflowID, "workflow", "", "Workflow ID to rerun (required if the run contains multiple workflows)")
	cmd.Flags().StringVar(&output, "output", "", "Output format (json)")

	return cmd
}
