package ci

import (
	"context"
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
		Use:   "logs <run-id | job-id | attempt-id | workflow-id>",
		Short: "Fetch logs for a CI job [beta]",
		Long: `Fetch and display log output for a CI job.

Accepts a run ID, job ID, attempt ID, or workflow ID. When given a run, job,
or workflow ID, the command resolves to the latest attempt automatically.
Use --job and --workflow to disambiguate when a run has multiple jobs.

This command is in beta and subject to change.`,
		Example: `  # Logs for a specific attempt
  depot ci logs <attempt-id>

  # Logs for a run (auto-selects job if only one)
  depot ci logs <run-id>

  # Logs for a workflow (auto-selects from the workflow's jobs)
  depot ci logs <workflow-id>

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
				// If the positional arg matches a workflow ID in the response,
				// auto-filter to that workflow's jobs.
				wfFilter := workflow
				if wfFilter == "" {
					for _, wf := range resp.Workflows {
						if wf.WorkflowId == id {
							wfFilter = wf.WorkflowPath
							break
						}
					}
				}

				attemptID, err := resolveAttempt(resp, id, job, wfFilter)
				if err != nil {
					return err
				}

				return printLogs(ctx, tokenVal, orgID, attemptID)
			}

			// Try resolving as a workflow ID by searching recent runs.
			resp, wfPath, wfErr := resolveWorkflow(ctx, tokenVal, orgID, id)
			if wfErr == nil {
				wfFilter := workflow
				if wfFilter == "" {
					wfFilter = wfPath
				}

				attemptID, err := resolveAttempt(resp, id, job, wfFilter)
				if err != nil {
					return err
				}

				return printLogs(ctx, tokenVal, orgID, attemptID)
			}

			// Fall back to treating the ID as an attempt ID directly.
			// Don't fall back if --job or --workflow were specified — those
			// only make sense for run-level resolution.
			if job != "" || workflow != "" {
				return fmt.Errorf("failed to look up run: %w\n  as workflow: %v", runErr, wfErr)
			}

			lines, err := api.CIGetJobAttemptLogs(ctx, tokenVal, orgID, id)
			if err != nil {
				// All paths failed — show errors so the user can
				// distinguish "bad ID" from "auth/network failure".
				return fmt.Errorf("could not resolve %q as a run, job, workflow, or attempt ID:\n  as run: %v\n  as workflow: %v\n  as attempt: %v", id, runErr, wfErr, err)
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

// jobKeyShort returns the short form of a job key (after the first colon),
// or the full key if there's no colon.
func jobKeyShort(key string) string {
	if i := strings.IndexByte(key, ':'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// jobDisplayNames computes a display name for each candidate. Uses the short
// name (after colon) when it's unique across all candidates, falls back to the
// full job key when there's a collision.
func jobDisplayNames(candidates []jobCandidate) map[string]string {
	// Count how many candidates share each short name.
	shortCounts := map[string]int{}
	for _, c := range candidates {
		shortCounts[jobKeyShort(c.job.JobKey)]++
	}

	names := make(map[string]string, len(candidates))
	for _, c := range candidates {
		short := jobKeyShort(c.job.JobKey)
		if shortCounts[short] > 1 {
			names[c.job.JobKey] = c.job.JobKey
		} else {
			names[c.job.JobKey] = short
		}
	}
	return names
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
			info = append(info, fmt.Sprintf("job %q from %s", jobKeyShort(targetJob.JobKey), workflowPath))
		} else {
			info = append(info, fmt.Sprintf("job %q", jobKeyShort(targetJob.JobKey)))
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

	// Match by job key (--job flag): exact match on full key or short name.
	if jobKey != "" {
		var exact, short []jobCandidate
		for _, c := range candidates {
			if c.job.JobKey == jobKey {
				exact = append(exact, c)
			} else if jobKeyShort(c.job.JobKey) == jobKey {
				short = append(short, c)
			}
		}

		matches := exact
		if len(matches) == 0 {
			matches = short
		}

		displayNames := jobDisplayNames(candidates)
		switch len(matches) {
		case 0:
			keys := make([]string, len(candidates))
			for i, c := range candidates {
				keys[i] = displayNames[c.job.JobKey]
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

	// Interactive fuzzy picker when terminal is available.
	if helpers.IsTerminal() {
		displayNames := jobDisplayNames(candidates)
		items := make([]PickJobItem, len(candidates))
		for i, c := range candidates {
			items[i] = PickJobItem{
				Name:     displayNames[c.job.JobKey],
				Status:   c.job.Status,
				Workflow: c.workflowPath,
				Index:    i,
			}
		}
		idx, err := PickJob(items)
		if err != nil {
			return nil, "", err
		}
		return candidates[idx].job, candidates[idx].workflowPath, nil
	}

	return nil, "", fmt.Errorf("run has multiple jobs, specify one with --job:\n%s", formatJobList(candidates))
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
// Uses short job names when unambiguous, full names when there are conflicts.
func formatJobList(candidates []jobCandidate) string {
	displayNames := jobDisplayNames(candidates)

	// Group by workflow path.
	type workflowGroup struct {
		label string
		jobs  []jobCandidate
	}
	var groups []workflowGroup
	groupIdx := map[string]int{}
	for _, c := range candidates {
		label := c.workflowPath
		if label == "" {
			label = c.workflowName
		}
		if idx, ok := groupIdx[label]; ok {
			groups[idx].jobs = append(groups[idx].jobs, c)
		} else {
			groupIdx[label] = len(groups)
			groups = append(groups, workflowGroup{label: label, jobs: []jobCandidate{c}})
		}
	}

	var b strings.Builder
	for _, g := range groups {
		fmt.Fprintf(&b, "\n  %s\n", g.label)
		for _, c := range g.jobs {
			fmt.Fprintf(&b, "    %s (%s)\n", displayNames[c.job.JobKey], c.job.Status)
		}
	}
	return b.String()
}

// printLogs fetches and prints all log lines for the given attempt.
func printLogs(ctx context.Context, token, orgID, attemptID string) error {
	lines, err := api.CIGetJobAttemptLogs(ctx, token, orgID, attemptID)
	if err != nil {
		return fmt.Errorf("failed to get logs: %w", err)
	}
	for _, line := range lines {
		fmt.Println(line.Body)
	}
	return nil
}

// resolveWorkflow searches recent runs for a workflow matching the given ID.
// Returns the run status, the matching workflow path, and any error.
func resolveWorkflow(ctx context.Context, token, orgID, workflowID string) (*civ1.GetRunStatusResponse, string, error) {
	allStatuses := []civ1.CIRunStatus{
		civ1.CIRunStatus_CI_RUN_STATUS_QUEUED,
		civ1.CIRunStatus_CI_RUN_STATUS_RUNNING,
		civ1.CIRunStatus_CI_RUN_STATUS_FINISHED,
		civ1.CIRunStatus_CI_RUN_STATUS_FAILED,
		civ1.CIRunStatus_CI_RUN_STATUS_CANCELLED,
	}

	runs, err := api.CIListRuns(ctx, token, orgID, allStatuses, 50)
	if err != nil {
		return nil, "", fmt.Errorf("failed to list runs: %w", err)
	}

	for _, run := range runs {
		resp, err := api.CIGetRunStatus(ctx, token, orgID, run.RunId)
		if err != nil {
			continue
		}
		for _, wf := range resp.Workflows {
			if wf.WorkflowId == workflowID {
				return resp, wf.WorkflowPath, nil
			}
		}
	}

	return nil, "", fmt.Errorf("workflow %q not found in recent runs", workflowID)
}
