package ci

import (
	"fmt"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdCancel() *cobra.Command {
	var (
		orgID      string
		token      string
		jobID      string
		workflowID string
		output     string
	)

	cmd := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel a CI run, workflow, or job",
		Long: `Cancel a queued or running CI run, workflow (and all its child jobs), or a single job within a workflow.

With no scope flags, the entire run is cancelled. Use --workflow to cancel an entire
workflow and all its jobs; use --job to cancel a single job within its workflow.`,
		Example: `  # Cancel a run
  depot ci cancel <run-id>

  # Cancel a workflow (and all its jobs)
  depot ci cancel <run-id> --workflow <workflow-id>

  # Cancel a single job in a workflow
  depot ci cancel <run-id> --job <job-id>

  # Output the RPC response as JSON
  depot ci cancel <run-id> --output json`,
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

			if jobID != "" {
				wfID, err := findWorkflowForJob(ctx, tokenVal, orgID, runID, jobID)
				if err != nil {
					return err
				}
				resp, err := api.CICancelJob(ctx, tokenVal, orgID, wfID, jobID)
				if err != nil {
					return fmt.Errorf("failed to cancel job: %w", err)
				}
				if output == "json" {
					return writeJSON(resp)
				}
				fmt.Printf("Cancelled job %s (%s)\n", resp.JobId, resp.Status)
				return nil
			}

			if workflowID == "" {
				resp, err := api.CICancelRun(ctx, tokenVal, orgID, runID)
				if err != nil {
					return fmt.Errorf("failed to cancel run: %w", err)
				}
				if output == "json" {
					return writeJSON(resp)
				}
				fmt.Printf("Cancelled run %s (%s)\n", resp.RunId, resp.Status)
				return nil
			}

			if err := validateWorkflowInRun(ctx, tokenVal, orgID, runID, workflowID); err != nil {
				return err
			}
			resp, err := api.CICancelWorkflow(ctx, tokenVal, orgID, workflowID)
			if err != nil {
				return fmt.Errorf("failed to cancel workflow: %w", err)
			}
			if output == "json" {
				return writeJSON(resp)
			}
			fmt.Printf("Cancelled workflow %s (%s)\n", resp.WorkflowId, resp.Status)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&workflowID, "workflow", "", "Workflow ID to cancel")
	cmd.Flags().StringVar(&jobID, "job", "", "Job ID to cancel within its workflow")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")

	cmd.MarkFlagsMutuallyExclusive("workflow", "job")

	return cmd
}
