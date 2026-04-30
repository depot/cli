package ci

import (
	"fmt"
	"strings"
	"time"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

var ciListWorkflows = api.CIListWorkflows
var ciGetWorkflow = api.CIGetWorkflow

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

type workflowShowJSON struct {
	OrgID      string                  `json:"org_id"`
	Run        workflowShowRunJSON     `json:"run"`
	Workflow   workflowShowMetaJSON    `json:"workflow"`
	Executions []workflowExecutionJSON `json:"executions"`
	Jobs       []workflowShowJobJSON   `json:"jobs"`
}

type workflowShowRunJSON struct {
	RunID      string `json:"run_id"`
	Repo       string `json:"repo"`
	Ref        string `json:"ref"`
	Sha        string `json:"sha"`
	HeadSha    string `json:"head_sha"`
	Trigger    string `json:"trigger"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

type workflowShowMetaJSON struct {
	WorkflowID   string `json:"workflow_id"`
	Name         string `json:"name"`
	WorkflowPath string `json:"workflow_path"`
	Status       string `json:"status"`
	ErrorMessage string `json:"error_message"`
	CreatedAt    string `json:"created_at"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
}

type workflowExecutionJSON struct {
	ExecutionID string `json:"execution_id"`
	Execution   int32  `json:"execution"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at"`
}

type workflowShowJobJSON struct {
	JobID      string                    `json:"job_id"`
	JobKey     string                    `json:"job_key"`
	Status     string                    `json:"status"`
	StartedAt  string                    `json:"started_at"`
	FinishedAt string                    `json:"finished_at"`
	Attempts   []workflowShowAttemptJSON `json:"attempts"`
}

type workflowShowAttemptJSON struct {
	AttemptID  string `json:"attempt_id"`
	Attempt    int32  `json:"attempt"`
	Status     string `json:"status"`
	SandboxID  string `json:"sandbox_id"`
	SessionID  string `json:"session_id"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
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
	cmd.AddCommand(NewCmdWorkflowShow())

	return cmd
}

func NewCmdWorkflowShow() *cobra.Command {
	var (
		orgID  string
		token  string
		output string
	)

	cmd := &cobra.Command{
		Use:     "show <workflow-id>",
		Aliases: []string{"get"},
		Short:   "Show a CI workflow",
		Long:    `Show a CI workflow, including parent run context, executions, jobs, and attempts.`,
		Example: `  depot ci workflow show <workflow-id>
  depot ci workflow get <workflow-id>
  depot ci workflow show <workflow-id> --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			workflowID := args[0]

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}
			orgFlag := ""
			if cmd.Flags().Changed("org") {
				orgFlag = " --org " + orgID
			}

			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			workflow, err := ciGetWorkflow(ctx, tokenVal, orgID, workflowID)
			if err != nil {
				return fmt.Errorf("failed to get workflow: %w", err)
			}

			if output == "json" {
				return writeJSON(workflowShowToJSON(workflow))
			}

			printWorkflow(workflow, orgFlag)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")

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

func workflowShowToJSON(workflow *civ1.GetWorkflowResponse) workflowShowJSON {
	out := workflowShowJSON{
		OrgID: workflow.GetOrgId(),
		Run: workflowShowRunJSON{
			RunID:      workflow.GetRunId(),
			Repo:       workflow.GetRepo(),
			Ref:        workflow.GetRef(),
			Sha:        workflow.GetSha(),
			HeadSha:    workflow.GetHeadSha(),
			Trigger:    workflow.GetTrigger(),
			Status:     workflow.GetRunStatus(),
			CreatedAt:  workflow.GetRunCreatedAt(),
			StartedAt:  workflow.GetRunStartedAt(),
			FinishedAt: workflow.GetRunFinishedAt(),
		},
		Workflow: workflowShowMetaJSON{
			WorkflowID:   workflow.GetWorkflowId(),
			Name:         workflow.GetWorkflowName(),
			WorkflowPath: workflow.GetWorkflowPath(),
			Status:       workflow.GetWorkflowStatus(),
			ErrorMessage: workflow.GetWorkflowErrorMessage(),
			CreatedAt:    workflow.GetWorkflowCreatedAt(),
			StartedAt:    workflow.GetWorkflowStartedAt(),
			FinishedAt:   workflow.GetWorkflowFinishedAt(),
		},
		Executions: make([]workflowExecutionJSON, 0, len(workflow.GetExecutions())),
		Jobs:       make([]workflowShowJobJSON, 0, len(workflow.GetJobs())),
	}

	for _, execution := range workflow.GetExecutions() {
		out.Executions = append(out.Executions, workflowExecutionJSON{
			ExecutionID: execution.GetExecutionId(),
			Execution:   execution.GetExecution(),
			Status:      execution.GetStatus(),
			CreatedAt:   execution.GetCreatedAt(),
			StartedAt:   execution.GetStartedAt(),
			FinishedAt:  execution.GetFinishedAt(),
		})
	}

	for _, job := range workflow.GetJobs() {
		outJob := workflowShowJobJSON{
			JobID:      job.GetJobId(),
			JobKey:     job.GetJobKey(),
			Status:     job.GetStatus(),
			StartedAt:  job.GetStartedAt(),
			FinishedAt: job.GetFinishedAt(),
			Attempts:   make([]workflowShowAttemptJSON, 0, len(job.GetAttempts())),
		}
		for _, attempt := range job.GetAttempts() {
			outJob.Attempts = append(outJob.Attempts, workflowShowAttemptJSON{
				AttemptID:  attempt.GetAttemptId(),
				Attempt:    attempt.GetAttempt(),
				Status:     attempt.GetStatus(),
				SandboxID:  attempt.GetSandboxId(),
				SessionID:  attempt.GetSessionId(),
				StartedAt:  attempt.GetStartedAt(),
				FinishedAt: attempt.GetFinishedAt(),
			})
		}
		out.Jobs = append(out.Jobs, outJob)
	}

	return out
}

func printWorkflow(workflow *civ1.GetWorkflowResponse, orgFlag string) {
	fmt.Printf("Org: %s\n", workflow.GetOrgId())
	fmt.Printf("Repo: %s\n", workflow.GetRepo())
	fmt.Printf("Run: %s (%s)\n", workflow.GetRunId(), workflow.GetRunStatus())
	fmt.Printf("Workflow: %s (%s)\n", workflow.GetWorkflowId(), workflow.GetWorkflowStatus())
	if workflow.GetWorkflowName() != "" {
		fmt.Printf("Name: %s\n", workflow.GetWorkflowName())
	}
	if workflow.GetWorkflowPath() != "" {
		fmt.Printf("Path: %s\n", workflow.GetWorkflowPath())
	}
	if workflow.GetWorkflowErrorMessage() != "" {
		fmt.Printf("Error: %s\n", workflow.GetWorkflowErrorMessage())
	}
	fmt.Printf("Ref: %s\n", workflow.GetRef())
	fmt.Printf("SHA: %s\n", workflowCommitSHAFromValues(workflow.GetSha(), workflow.GetHeadSha()))
	fmt.Printf("Trigger: %s\n", workflow.GetTrigger())

	fmt.Println()
	fmt.Println("Executions:")
	if len(workflow.GetExecutions()) == 0 {
		fmt.Println("  none")
	} else {
		for _, execution := range workflow.GetExecutions() {
			duration := formatDuration(execution.GetStartedAt(), execution.GetFinishedAt())
			fmt.Printf(
				"  #%d %s%s  %s -> %s\n",
				execution.GetExecution(),
				execution.GetStatus(),
				durationSuffix(duration),
				emptyTimestamp(execution.GetStartedAt()),
				emptyTimestamp(execution.GetFinishedAt()),
			)
		}
	}

	fmt.Println()
	fmt.Println("Jobs:")
	if len(workflow.GetJobs()) == 0 {
		fmt.Println("  none")
		return
	}

	for _, job := range workflow.GetJobs() {
		duration := formatDuration(job.GetStartedAt(), job.GetFinishedAt())
		fmt.Printf("  %s [%s]%s\n", jobKeyShort(job.GetJobKey()), job.GetStatus(), durationSuffix(duration))
		fmt.Printf("    Job ID: %s\n", job.GetJobId())

		latest := latestWorkflowAttempt(job.GetAttempts())
		if latest == nil {
			fmt.Println("    Latest attempt: none")
			continue
		}

		latestDuration := formatDuration(latest.GetStartedAt(), latest.GetFinishedAt())
		fmt.Printf(
			"    Latest attempt: #%d %s (%s)%s\n",
			latest.GetAttempt(),
			latest.GetAttemptId(),
			latest.GetStatus(),
			durationSuffix(latestDuration),
		)
		if latest.GetSandboxId() != "" {
			fmt.Printf("    Sandbox: %s\n", latest.GetSandboxId())
		}
		if latest.GetSessionId() != "" {
			fmt.Printf("    Session: %s\n", latest.GetSessionId())
		}
		if len(job.GetAttempts()) > 1 {
			fmt.Println("    Attempts:")
			for _, attempt := range job.GetAttempts() {
				attemptDuration := formatDuration(attempt.GetStartedAt(), attempt.GetFinishedAt())
				fmt.Printf(
					"      #%d %s (%s)%s\n",
					attempt.GetAttempt(),
					attempt.GetAttemptId(),
					attempt.GetStatus(),
					durationSuffix(attemptDuration),
				)
			}
		}
		fmt.Printf("    Logs: depot ci logs %s%s\n", latest.GetAttemptId(), orgFlag)
	}
}

func latestWorkflowAttempt(attempts []*civ1.GetWorkflowJobAttempt) *civ1.GetWorkflowJobAttempt {
	if len(attempts) == 0 {
		return nil
	}
	latest := attempts[0]
	for _, attempt := range attempts[1:] {
		if attempt.GetAttempt() > latest.GetAttempt() {
			latest = attempt
		}
	}
	return latest
}

func formatDuration(startedAt, finishedAt string) string {
	start, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return ""
	}
	finish, err := time.Parse(time.RFC3339Nano, finishedAt)
	if err != nil {
		return ""
	}
	if finish.Before(start) {
		return ""
	}
	return formatCompactDuration(finish.Sub(start))
}

func formatCompactDuration(duration time.Duration) string {
	seconds := int64(duration.Round(time.Second).Seconds())
	if seconds < 0 {
		return ""
	}

	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60

	if hours > 0 {
		return fmt.Sprintf("%dh%dm%ds", hours, minutes, secs)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm%ds", minutes, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

func durationSuffix(duration string) string {
	if duration == "" {
		return ""
	}
	return " " + duration
}

func emptyTimestamp(timestamp string) string {
	if timestamp == "" {
		return "-"
	}
	return timestamp
}

func workflowCommitSHAFromValues(sha, headSha string) string {
	if headSha != "" {
		return headSha
	}
	return sha
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
