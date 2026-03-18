package ci

import (
	"fmt"
	"os"
	"strings"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

func NewCmdLogs() *cobra.Command {
	var (
		orgID    string
		token    string
		job      string
		workflow string
	)

	cmd := &cobra.Command{
		Use:   "logs <run-id | job-id | attempt-id>",
		Short: "Fetch logs for a CI job [beta]",
		Long: `Fetch and display log output for a CI job.

Accepts a run ID, job ID, or attempt ID. When given a run or job ID, the
command resolves to the latest attempt automatically. Use --job and --workflow
to disambiguate when a run has multiple jobs.

This command is in beta and subject to change.`,
		Example: `  # Logs for a specific attempt
  depot ci logs <attempt-id>

  # Logs for a run (auto-selects job if only one)
  depot ci logs <run-id>

  # Logs for a specific job in a run
  depot ci logs <run-id> --job test

  # Disambiguate when multiple workflows define the same job key
  depot ci logs <run-id> --job build --workflow ci.yml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}

			ctx := cmd.Context()
			id := args[0]

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

			// First, try resolving as a run ID (or job ID — the API accepts both).
			resp, runErr := api.CIGetRunStatus(ctx, tokenVal, orgID, id)
			if runErr == nil {
				attemptID, err := resolveAttempt(resp, id, job, workflow)
				if err != nil {
					return err
				}

				lines, err := api.CIGetJobAttemptLogs(ctx, tokenVal, orgID, attemptID)
				if err != nil {
					return fmt.Errorf("failed to get logs: %w", err)
				}

				for _, line := range lines {
					fmt.Println(line.Body)
				}
				return nil
			}

			// Fall back to treating the ID as an attempt ID directly.
			lines, err := api.CIGetJobAttemptLogs(ctx, tokenVal, orgID, id)
			if err != nil {
				return fmt.Errorf("could not resolve %q as a run, job, or attempt ID", id)
			}

			for _, line := range lines {
				fmt.Println(line.Body)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&job, "job", "", "Job key to select (required when run has multiple jobs)")
	cmd.Flags().StringVar(&workflow, "workflow", "", "Workflow path to filter jobs (e.g. ci.yml)")

	return cmd
}

type jobCandidate struct {
	job          *civ1.JobStatus
	workflowPath string
	workflowName string
}

// resolveAttempt finds the target attempt from a run status response.
// It selects a job (by --job flag, by job ID match, or auto-select), then
// picks the latest attempt and prints informational messages about what was chosen.
func resolveAttempt(resp *civ1.GetRunStatusResponse, originalID, jobKey, workflowFilter string) (string, error) {
	targetJob, workflowPath, err := findLogsJob(resp, originalID, jobKey, workflowFilter)
	if err != nil {
		return "", err
	}

	if len(targetJob.Attempts) == 0 {
		return "", fmt.Errorf("job %q has no attempts yet", targetJob.JobKey)
	}

	latest := targetJob.Attempts[0]
	for _, a := range targetJob.Attempts[1:] {
		if a.Attempt > latest.Attempt {
			latest = a
		}
	}

	// Print what we auto-selected so the user knows.
	var info []string
	if jobKey == "" {
		if workflowPath != "" {
			info = append(info, fmt.Sprintf("job %q from %s", targetJob.JobKey, workflowPath))
		} else {
			info = append(info, fmt.Sprintf("job %q", targetJob.JobKey))
		}
	}

	if len(targetJob.Attempts) > 1 {
		var others []string
		for _, a := range targetJob.Attempts {
			if a.AttemptId != latest.AttemptId {
				others = append(others, fmt.Sprintf("#%d %s", a.Attempt, a.AttemptId))
			}
		}
		info = append(info, fmt.Sprintf("attempt #%d (also available: %s)", latest.Attempt, strings.Join(others, ", ")))
	}

	if len(info) > 0 {
		fmt.Fprintf(os.Stderr, "Using %s\n", strings.Join(info, ", "))
	}

	return latest.AttemptId, nil
}

// findLogsJob locates the target job in the run status response.
// Returns the job and the workflow path it belongs to.
func findLogsJob(resp *civ1.GetRunStatusResponse, originalID, jobKey, workflowFilter string) (*civ1.JobStatus, string, error) {
	var candidates []jobCandidate
	for _, wf := range resp.Workflows {
		if workflowFilter != "" && !workflowPathMatches(wf.WorkflowPath, workflowFilter) {
			continue
		}
		for _, j := range wf.Jobs {
			candidates = append(candidates, jobCandidate{
				job:          j,
				workflowPath: wf.WorkflowPath,
				workflowName: wf.Name,
			})
		}
	}

	if len(candidates) == 0 {
		if workflowFilter != "" {
			return nil, "", fmt.Errorf("no jobs found in workflow %q", workflowFilter)
		}
		return nil, "", fmt.Errorf("run %s has no jobs", resp.RunId)
	}

	// Match by job key (--job flag).
	if jobKey != "" {
		var matches []jobCandidate
		for _, c := range candidates {
			if c.job.JobKey == jobKey {
				matches = append(matches, c)
			}
		}
		switch len(matches) {
		case 0:
			keys := make([]string, len(candidates))
			for i, c := range candidates {
				keys[i] = c.job.JobKey
			}
			return nil, "", fmt.Errorf("job %q not found (available: %s)", jobKey, strings.Join(keys, ", "))
		case 1:
			return matches[0].job, matches[0].workflowPath, nil
		default:
			// Same job key in multiple workflows — need --workflow.
			var paths []string
			for _, m := range matches {
				paths = append(paths, m.workflowPath)
			}
			return nil, "", fmt.Errorf("job %q exists in multiple workflows, specify one with --workflow: %s", jobKey, strings.Join(paths, ", "))
		}
	}

	// Match by job ID (user may have passed a job ID as the positional arg).
	for _, c := range candidates {
		if c.job.JobId == originalID {
			return c.job, c.workflowPath, nil
		}
	}

	// Auto-select if there's exactly one job.
	if len(candidates) == 1 {
		return candidates[0].job, candidates[0].workflowPath, nil
	}

	return nil, "", fmt.Errorf("run has multiple jobs, specify one with --job:\n%s", formatJobList(resp, workflowFilter))
}

// workflowPathMatches checks if a workflow path matches the filter.
// The filter can be a full path or just the filename.
func workflowPathMatches(path, filter string) bool {
	if path == filter {
		return true
	}
	// Allow matching by filename only (e.g. "ci.yml" matches ".depot/workflows/ci.yml").
	if strings.HasSuffix(path, "/"+filter) {
		return true
	}
	return false
}

// formatJobList returns a string listing jobs grouped by workflow for error messages.
func formatJobList(resp *civ1.GetRunStatusResponse, workflowFilter string) string {
	var b strings.Builder
	for _, wf := range resp.Workflows {
		if workflowFilter != "" && !workflowPathMatches(wf.WorkflowPath, workflowFilter) {
			continue
		}
		if len(wf.Jobs) == 0 {
			continue
		}
		label := wf.WorkflowPath
		if label == "" {
			label = wf.Name
		}
		if label == "" {
			label = wf.WorkflowId
		}
		fmt.Fprintf(&b, "\n  %s\n", label)
		for _, j := range wf.Jobs {
			fmt.Fprintf(&b, "    %s (%s)\n", j.JobKey, j.Status)
		}
	}
	return b.String()
}
