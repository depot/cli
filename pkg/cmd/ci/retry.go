package ci

import (
	"fmt"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdRetry() *cobra.Command {
	var (
		orgID      string
		token      string
		jobID      string
		workflowID string
		failed     bool
		output     string
	)

	cmd := &cobra.Command{
		Use:   "retry <run-id>",
		Short: "Retry a failed CI job, or all failed jobs in a workflow",
		Long: `Retry a single failed job with --job, or retry every failed/cancelled job in a workflow with --failed.

Exactly one of --job or --failed must be set. --failed requires --workflow unless the run contains
only a single workflow; --job resolves its containing workflow from the run automatically.`,
		Example: `  # Retry a single failed job
  depot ci retry <run-id> --job <job-id>

  # Retry all failed jobs in the only workflow
  depot ci retry <run-id> --failed

  # Retry all failed jobs in a specific workflow
  depot ci retry <run-id> --failed --workflow <workflow-id>

  # Output the RPC response as JSON
  depot ci retry <run-id> --job <job-id> --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			runID := args[0]

			if jobID == "" && !failed {
				return fmt.Errorf("one of --job or --failed must be provided")
			}

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

			if failed {
				wfID := workflowID
				if wfID == "" {
					wfID, err = resolveSingleWorkflow(ctx, tokenVal, orgID, runID)
					if err != nil {
						return err
					}
				}
				resp, err := api.CIRetryFailedJobs(ctx, tokenVal, orgID, wfID)
				if err != nil {
					return fmt.Errorf("failed to retry failed jobs: %w", err)
				}
				if output == "json" {
					return writeJSON(resp)
				}
				fmt.Printf("Retrying %d failed jobs in workflow %s\n", resp.JobCount, resp.WorkflowId)
				for _, id := range resp.JobIds {
					fmt.Printf("  Job: %s\n", id)
				}
				return nil
			}

			wfID := workflowID
			if wfID == "" {
				wfID, err = findWorkflowForJob(ctx, tokenVal, orgID, runID, jobID)
				if err != nil {
					return err
				}
			}
			resp, err := api.CIRetryJob(ctx, tokenVal, orgID, wfID, jobID)
			if err != nil {
				return fmt.Errorf("failed to retry job: %w", err)
			}
			if output == "json" {
				return writeJSON(resp)
			}
			fmt.Printf("Retrying job %s (attempt #%d, %s)\n", resp.JobId, resp.Attempt, resp.Status)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&jobID, "job", "", "Job ID to retry")
	cmd.Flags().BoolVar(&failed, "failed", false, "Retry every failed or cancelled job in the workflow")
	cmd.Flags().StringVar(&workflowID, "workflow", "", "Workflow ID (required with --failed if the run has multiple workflows)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")

	cmd.MarkFlagsMutuallyExclusive("job", "failed")

	return cmd
}
