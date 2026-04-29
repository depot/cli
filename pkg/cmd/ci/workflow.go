package ci

import (
	"fmt"
	"strings"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

var ciListWorkflows = api.CIListWorkflows

type workflowListJSON struct {
	WorkflowID   string                `json:"workflow_id"`
	Name         string                `json:"name"`
	WorkflowPath string                `json:"workflow_path"`
	Repo         string                `json:"repo"`
	Status       string                `json:"status"`
	Trigger      string                `json:"trigger"`
	RunID        string                `json:"run_id"`
	Sha          string                `json:"sha"`
	HeadSha      string                `json:"head_sha"`
	CreatedAt    string                `json:"created_at"`
	JobCounts    workflowJobCountsJSON `json:"job_counts"`
}

type workflowJobCountsJSON struct {
	Total     int32 `json:"total"`
	Queued    int32 `json:"queued"`
	Waiting   int32 `json:"waiting"`
	Running   int32 `json:"running"`
	Finished  int32 `json:"finished"`
	Failed    int32 `json:"failed"`
	Cancelled int32 `json:"cancelled"`
	Skipped   int32 `json:"skipped"`
}

func NewCmdWorkflow() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Manage CI workflows",
		Long:  "Manage CI workflows.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(NewCmdWorkflowList())

	return cmd
}

func NewCmdWorkflowList() *cobra.Command {
	var (
		orgID   string
		token   string
		n       int32
		output  string
		name    string
		repo    string
		status  []string
		trigger string
		sha     string
		pr      string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List CI workflows",
		Long:  `List recent CI workflows for your organization.`,
		Example: `  # List recent workflows
  depot ci workflow list

  # List the 5 most recent workflows
  depot ci workflow list -n 5

  # Filter workflows by name
  depot ci workflow list --name deploy

  # Filter recent workflows like the app
  depot ci workflow list --repo depot/api --status failed --sha abc123 --pr 42

  # Output as JSON
  depot ci workflow list --output json`,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if n <= 0 {
				return fmt.Errorf("page size (-n) must be greater than 0")
			}
			if n > 200 {
				return fmt.Errorf("page size (-n) must be 200 or less")
			}
			for _, s := range status {
				if err := validateStatus(s); err != nil {
					return err
				}
			}

			ctx := cmd.Context()

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

			workflows, err := ciListWorkflows(ctx, tokenVal, orgID, api.CIListWorkflowsOptions{
				Limit:       n,
				Name:        name,
				Repo:        repo,
				Statuses:    status,
				Trigger:     trigger,
				Sha:         sha,
				PullRequest: pr,
			})
			if err != nil {
				return fmt.Errorf("failed to list workflows: %w", err)
			}

			if output == "json" {
				return writeJSON(workflowsToJSON(workflows))
			}

			if len(workflows) == 0 {
				fmt.Println("No workflows found.")
				return nil
			}

			fmt.Printf("%-24s %-30s %-30s %-10s %-12s %-12s %-24s %s\n", "WORKFLOW ID", "NAME/PATH", "REPO", "STATUS", "TRIGGER", "SHA", "RUN ID", "CREATED")
			fmt.Printf("%-24s %-30s %-30s %-10s %-12s %-12s %-24s %s\n",
				strings.Repeat("-", 24),
				strings.Repeat("-", 30),
				strings.Repeat("-", 30),
				strings.Repeat("-", 10),
				strings.Repeat("-", 12),
				strings.Repeat("-", 12),
				strings.Repeat("-", 24),
				strings.Repeat("-", 20),
			)

			for _, workflow := range workflows {
				fmt.Printf("%-24s %-30s %-30s %-10s %-12s %-12s %-24s %s\n",
					workflow.WorkflowId,
					truncate(workflowDisplayName(workflow), 30),
					truncate(workflow.Repo, 30),
					workflow.Status,
					truncate(workflow.Trigger, 12),
					truncate(workflowCommitSHA(workflow), 12),
					workflow.RunId,
					workflow.CreatedAt,
				)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().Int32VarP(&n, "n", "n", 50, "Number of recent workflows to return (max 200)")
	cmd.Flags().StringVar(&name, "name", "", "Filter workflows by name")
	cmd.Flags().StringVar(&repo, "repo", "", "Filter by repo in owner/name format")
	cmd.Flags().StringSliceVar(&status, "status", nil, "Filter by status (repeatable: queued, running, finished, failed, cancelled)")
	cmd.Flags().StringVar(&trigger, "trigger", "", "Filter by trigger, e.g. push or workflow_dispatch")
	cmd.Flags().StringVar(&sha, "sha", "", "Filter by head SHA prefix")
	cmd.Flags().StringVar(&pr, "pr", "", "Filter by pull request number")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")

	return cmd
}

func workflowsToJSON(workflows []*civ1.ListWorkflowsResponseWorkflow) []workflowListJSON {
	out := make([]workflowListJSON, 0, len(workflows))
	for _, workflow := range workflows {
		counts := workflow.GetJobCounts()
		out = append(out, workflowListJSON{
			WorkflowID:   workflow.GetWorkflowId(),
			Name:         workflow.GetName(),
			WorkflowPath: workflow.GetWorkflowPath(),
			Repo:         workflow.GetRepo(),
			Status:       workflow.GetStatus(),
			Trigger:      workflow.GetTrigger(),
			RunID:        workflow.GetRunId(),
			Sha:          workflow.GetSha(),
			HeadSha:      workflow.GetHeadSha(),
			CreatedAt:    workflow.GetCreatedAt(),
			JobCounts: workflowJobCountsJSON{
				Total:     counts.GetTotal(),
				Queued:    counts.GetQueued(),
				Waiting:   counts.GetWaiting(),
				Running:   counts.GetRunning(),
				Finished:  counts.GetFinished(),
				Failed:    counts.GetFailed(),
				Cancelled: counts.GetCancelled(),
				Skipped:   counts.GetSkipped(),
			},
		})
	}
	return out
}

func workflowDisplayName(workflow *civ1.ListWorkflowsResponseWorkflow) string {
	if workflow.GetName() != "" {
		return workflow.GetName()
	}
	if workflow.GetWorkflowPath() != "" {
		return workflow.GetWorkflowPath()
	}
	return "(unnamed)"
}

func workflowCommitSHA(workflow *civ1.ListWorkflowsResponseWorkflow) string {
	if workflow.GetHeadSha() != "" {
		return workflow.GetHeadSha()
	}
	return workflow.GetSha()
}

func truncate(value string, maxLen int) string {
	if len(value) <= maxLen {
		return value
	}
	if maxLen <= 3 {
		return value[:maxLen]
	}
	return value[:maxLen-3] + "..."
}
