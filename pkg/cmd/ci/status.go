package ci

import (
	"fmt"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdStatus() *cobra.Command {
	var (
		orgID string
		token string
	)

	cmd := &cobra.Command{
		Use:   "status <run-id>",
		Short: "Look up the status of a CI run [beta]",
		Long:  "Look up the status of a CI run, including its workflows, jobs, and attempts.\n\nThis command is in beta and subject to change.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}

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

			resp, err := api.CIGetRunStatus(ctx, tokenVal, orgID, runID)
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

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")

	return cmd
}
