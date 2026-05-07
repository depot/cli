package ci

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

func NewCmdStatus() *cobra.Command {
	var (
		orgID  string
		token  string
		output string
	)

	cmd := &cobra.Command{
		Use:   "status <run-id>",
		Short: "Look up the status of a CI run",
		Long:  "Look up the status of a CI run, including its workflows, jobs, and attempts.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}

			ctx := cmd.Context()
			runID := args[0]

			switch output {
			case "", "json":
			default:
				return fmt.Errorf("unsupported output %q (valid: json)", output)
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

			resp, err := ciGetRunStatus(ctx, tokenVal, orgID, runID)
			if err != nil {
				return fmt.Errorf("failed to get run status: %w", err)
			}

			orgFlag := ""
			if cmd.Flags().Changed("org") {
				orgFlag = " --org " + orgID
			}
			if output == "json" {
				return writeJSON(statusToJSON(resp, orgFlag))
			}

			fmt.Printf("Org: %s\n", resp.OrgId)
			fmt.Printf("Run: %s (%s)\n", resp.RunId, resp.Status)

			for _, workflow := range resp.Workflows {
				fmt.Println()
				fmt.Printf("  Workflow: %s (%s)\n", workflow.WorkflowId, workflow.Status)
				if workflow.WorkflowPath != "" {
					fmt.Printf("    Path: %s\n", workflow.WorkflowPath)
				}
				if workflow.ErrorMessage != "" {
					fmt.Printf("    Error: %s\n", workflow.ErrorMessage)
				}

				for _, job := range workflow.Jobs {
					fmt.Printf("    Job: %s [%s] (%s)\n", job.JobId, job.JobKey, job.Status)

					for _, attempt := range job.Attempts {
						fmt.Printf("      Attempt #%d (%s)\n", attempt.Attempt, attempt.Status)
						fmt.Printf("        Logs: depot ci logs %s%s\n", attempt.AttemptId, orgFlag)
						if canDownloadLogExport(attempt.Status) {
							fmt.Printf("        Download: depot ci logs %s --output-file %s%s\n", attempt.AttemptId, logDownloadFilename, orgFlag)
						}
						fmt.Printf("        View: %s\n", statusAttemptViewURL(resp.OrgId, workflow.WorkflowId, job.JobId, attempt.AttemptId))
						if attempt.GetSandboxId() != "" && statusIsRunning(attempt.GetStatus()) {
							fmt.Printf("        SSH:  depot ci ssh %s --job %s%s\n", resp.RunId, job.JobKey, orgFlag)
						}
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")

	return cmd
}

type statusJSON struct {
	OrgID     string               `json:"org_id"`
	RunID     string               `json:"run_id"`
	Status    string               `json:"status"`
	Workflows []statusWorkflowJSON `json:"workflows"`
}

type statusWorkflowJSON struct {
	WorkflowID   string          `json:"workflow_id"`
	Status       string          `json:"status"`
	WorkflowPath string          `json:"workflow_path,omitempty"`
	Name         string          `json:"name,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
	Jobs         []statusJobJSON `json:"jobs"`
}

type statusJobJSON struct {
	JobID    string              `json:"job_id"`
	JobKey   string              `json:"job_key"`
	Status   string              `json:"status"`
	Attempts []statusAttemptJSON `json:"attempts"`
}

type statusAttemptJSON struct {
	AttemptID         string `json:"attempt_id"`
	Attempt           int32  `json:"attempt"`
	Status            string `json:"status"`
	SandboxID         string `json:"sandbox_id,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	LogsCommand       string `json:"logs_command"`
	DownloadAvailable bool   `json:"download_available"`
	DownloadCommand   string `json:"download_command,omitempty"`
	ViewURL           string `json:"view_url"`
	SSHAvailable      bool   `json:"ssh_available"`
	SSHCommand        string `json:"ssh_command,omitempty"`
}

func statusToJSON(resp *civ1.GetRunStatusResponse, orgFlag string) statusJSON {
	out := statusJSON{
		OrgID:     resp.GetOrgId(),
		RunID:     resp.GetRunId(),
		Status:    resp.GetStatus(),
		Workflows: make([]statusWorkflowJSON, 0, len(resp.GetWorkflows())),
	}

	for _, workflow := range resp.GetWorkflows() {
		wf := statusWorkflowJSON{
			WorkflowID:   workflow.GetWorkflowId(),
			Status:       workflow.GetStatus(),
			WorkflowPath: workflow.GetWorkflowPath(),
			Name:         workflow.GetName(),
			ErrorMessage: workflow.GetErrorMessage(),
			Jobs:         make([]statusJobJSON, 0, len(workflow.GetJobs())),
		}

		for _, job := range workflow.GetJobs() {
			j := statusJobJSON{
				JobID:    job.GetJobId(),
				JobKey:   job.GetJobKey(),
				Status:   job.GetStatus(),
				Attempts: make([]statusAttemptJSON, 0, len(job.GetAttempts())),
			}

			for _, attempt := range job.GetAttempts() {
				a := statusAttemptJSON{
					AttemptID:         attempt.GetAttemptId(),
					Attempt:           attempt.GetAttempt(),
					Status:            attempt.GetStatus(),
					SandboxID:         attempt.GetSandboxId(),
					SessionID:         attempt.GetSessionId(),
					LogsCommand:       fmt.Sprintf("depot ci logs %s%s", attempt.GetAttemptId(), orgFlag),
					DownloadAvailable: canDownloadLogExport(attempt.GetStatus()),
					ViewURL:           statusAttemptViewURL(resp.GetOrgId(), workflow.GetWorkflowId(), job.GetJobId(), attempt.GetAttemptId()),
					SSHAvailable:      attempt.GetSandboxId() != "" && statusIsRunning(attempt.GetStatus()),
				}
				if a.DownloadAvailable {
					a.DownloadCommand = fmt.Sprintf("depot ci logs %s --output-file %s%s", attempt.GetAttemptId(), logDownloadFilename, orgFlag)
				}
				if a.SSHAvailable {
					a.SSHCommand = fmt.Sprintf("depot ci ssh %s --job %s%s", resp.GetRunId(), job.GetJobKey(), orgFlag)
				}
				j.Attempts = append(j.Attempts, a)
			}
			wf.Jobs = append(wf.Jobs, j)
		}
		out.Workflows = append(out.Workflows, wf)
	}

	return out
}

func statusIsRunning(status string) bool {
	return status != "finished" && status != "failed" && status != "cancelled"
}

func statusAttemptViewURL(orgID, workflowID, jobID, attemptID string) string {
	path := fmt.Sprintf("https://depot.dev/orgs/%s/workflows/%s", url.PathEscape(orgID), url.PathEscape(workflowID))

	var params []string
	if jobID != "" {
		params = append(params, "job="+url.QueryEscape(jobID))
	}
	if attemptID != "" {
		params = append(params, "attempt="+url.QueryEscape(attemptID))
	}
	if len(params) == 0 {
		return path
	}
	return path + "?" + strings.Join(params, "&")
}
