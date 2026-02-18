package ci

import (
	"fmt"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdStatus() *cobra.Command {
	var token string

	cmd := &cobra.Command{
		Use:   "status <run-id>",
		Short: "Look up the status of a CI run",
		Long:  "Look up the status of a CI run, including its workflows, jobs, and attempts.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			runID := args[0]

			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			resp, err := api.CIGetRunStatus(ctx, tokenVal, runID)
			if err != nil {
				return fmt.Errorf("failed to get run status: %w", err)
			}

			fmt.Printf("Org: %s\n", resp.OrgId)
			fmt.Printf("Run: %s (%s)\n", resp.RunId, resp.Status)

			for _, workflow := range resp.Workflows {
				fmt.Println()
				fmt.Printf("  Workflow: %s (%s)\n", workflow.WorkflowId, workflow.Status)
				if workflow.WorkflowPath != "" {
					fmt.Printf("    Path: %s\n", workflow.WorkflowPath)
				}

				for _, job := range workflow.Jobs {
					fmt.Printf("    Job: %s [%s] (%s)\n", job.JobId, job.JobKey, job.Status)

					for _, attempt := range job.Attempts {
						fmt.Printf("      Attempt: %s #%d (%s)\n", attempt.AttemptId, attempt.Attempt, attempt.Status)
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&token, "token", "", "Depot API token")

	return cmd
}
