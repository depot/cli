package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/pty"
	"github.com/spf13/cobra"
)

func NewCmdSSH() *cobra.Command {
	var (
		orgID  string
		token  string
		job    string
		info   bool
		output string
	)

	cmd := &cobra.Command{
		Use:   "ssh <run-id | job-id>",
		Short: "Connect to a running CI job via interactive terminal [beta]",
		Long: `Open an interactive terminal session to the sandbox running a CI job.

Accepts either a run ID (with optional --job flag) or a job ID directly.
If the job hasn't started yet, the command will wait for the sandbox to be provisioned.
Use --info to print SSH connection details instead of connecting interactively.

This command is in beta and subject to change.`,
		Example: `  # Connect directly using a job ID
  depot ci ssh <job-id>

  # Connect to a specific job in a run
  depot ci ssh <run-id> --job build

  # Auto-select job when there's only one
  depot ci ssh <run-id>

  # Print SSH connection details (for agents/automation)
  depot ci ssh <run-id> --info --output json`,
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

			sandboxID, sessionID, err := waitForSandbox(ctx, tokenVal, orgID, runID, job, runID)
			if err != nil {
				return err
			}

			if info || !helpers.IsTerminal() {
				return printSSHInfo(sandboxID, sessionID, output)
			}

			return pty.Run(ctx, pty.SessionOptions{
				Token:     tokenVal,
				SandboxID: sandboxID,
				SessionID: sessionID,
			})
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&job, "job", "", "Job key to connect to (required when run has multiple jobs)")
	cmd.Flags().BoolVar(&info, "info", false, "Print SSH connection details instead of connecting")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format for --info (json)")

	return cmd
}

// waitForSandbox polls the CI run status until a sandbox_id is available for the
// target job, or returns an error if the job has finished or doesn't exist.
// originalID is the raw ID the user passed — it may be a run ID or a job ID.
// When jobKey is empty, we also try matching jobs by ID using originalID.
func waitForSandbox(ctx context.Context, token, orgID, runID, jobKey, originalID string) (sandboxID, sessionID string, err error) {
	const pollInterval = 2 * time.Second
	const timeout = 5 * time.Minute

	const (
		stateInit = iota
		stateWaitingForJob
		stateWaitingForStart
		stateWaitingForSandbox
	)

	deadline := time.Now().Add(timeout)
	currentState := stateInit

	for {
		if time.Now().After(deadline) {
			return "", "", fmt.Errorf("timed out waiting for sandbox to be provisioned (waited %s)", timeout)
		}

		resp, err := api.CIGetRunStatus(ctx, token, orgID, runID)
		if err != nil {
			return "", "", fmt.Errorf("failed to get run status: %w", err)
		}

		targetJob, err := findJob(resp, jobKey, originalID)
		if err == nil && jobKey == "" {
			// Latch the auto-selected job key so subsequent polls don't
			// fail if more jobs appear while we wait for the sandbox.
			jobKey = targetJob.JobKey
		}
		if err != nil {
			// If no jobs exist yet or the target job hasn't appeared, keep polling
			// — but only if the run itself is still active.
			if isRetryableJobError(err) {
				if resp.Status == "finished" || resp.Status == "failed" || resp.Status == "cancelled" {
					return "", "", fmt.Errorf("%s (run status: %s)", err, resp.Status)
				}
				if currentState != stateWaitingForJob {
					fmt.Fprintf(os.Stderr, "Waiting for job to be created...\n")
					currentState = stateWaitingForJob
				}
				select {
				case <-ctx.Done():
					return "", "", ctx.Err()
				case <-time.After(pollInterval):
				}
				continue
			}
			return "", "", err
		}

		attempt := latestAttempt(targetJob)
		if attempt == nil {
			if currentState != stateWaitingForStart {
				fmt.Fprintf(os.Stderr, "Waiting for job %q to start...\n", targetJob.JobKey)
				currentState = stateWaitingForStart
			}
		} else {
			switch attempt.Status {
			case "finished", "failed", "cancelled":
				return "", "", fmt.Errorf("job %q has already completed (status: %s)", targetJob.JobKey, attempt.Status)
			default:
				sid := attempt.GetSandboxId()
				ssid := attempt.GetSessionId()
				if sid != "" && ssid != "" {
					fmt.Fprintf(os.Stderr, "Connecting to sandbox %s...\n", sid)
					return sid, ssid, nil
				}
				if currentState != stateWaitingForSandbox {
					fmt.Fprintf(os.Stderr, "Waiting for sandbox to be provisioned...\n")
					currentState = stateWaitingForSandbox
				}
			}
		}

		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// retryableJobError is returned when jobs haven't been created yet.
type retryableJobError struct{ msg string }

func (e *retryableJobError) Error() string { return e.msg }

func isRetryableJobError(err error) bool {
	var re *retryableJobError
	return errors.As(err, &re)
}

// findJob locates the target job in the run status response.
// It tries matching by job key (--job flag), then by job ID (originalID),
// then auto-selects if there's exactly one job.
func findJob(resp *civ1.GetRunStatusResponse, jobKey, originalID string) (*civ1.JobStatus, error) {
	var allJobs []*civ1.JobStatus
	for _, wf := range resp.Workflows {
		allJobs = append(allJobs, wf.Jobs...)
	}

	if len(allJobs) == 0 {
		return nil, &retryableJobError{msg: fmt.Sprintf("run %s has no jobs yet", resp.RunId)}
	}

	// Match by job key (--job flag).
	if jobKey != "" {
		for _, j := range allJobs {
			if j.JobKey == jobKey {
				return j, nil
			}
		}
		// Job might not exist yet if workflows are still being expanded.
		return nil, &retryableJobError{msg: fmt.Sprintf("job %q not found yet", jobKey)}
	}

	// Match by job ID (user passed a job ID as the positional arg).
	if originalID != "" {
		for _, j := range allJobs {
			if j.JobId == originalID {
				return j, nil
			}
		}
	}

	// Auto-select if there's only one job.
	if len(allJobs) == 1 {
		return allJobs[0], nil
	}

	keys := make([]string, len(allJobs))
	for i, j := range allJobs {
		keys[i] = fmt.Sprintf("  %s (%s)", j.JobKey, j.Status)
	}
	return nil, fmt.Errorf("run has multiple jobs, specify one with --job:\n%s", strings.Join(keys, "\n"))
}

func latestAttempt(job *civ1.JobStatus) *civ1.AttemptStatus {
	if len(job.Attempts) == 0 {
		return nil
	}
	latest := job.Attempts[0]
	for _, a := range job.Attempts[1:] {
		if a.Attempt > latest.Attempt {
			latest = a
		}
	}
	return latest
}

func printSSHInfo(sandboxID, sessionID, output string) error {
	if output == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]string{
			"host":        "api.depot.dev",
			"sandbox_id":  sandboxID,
			"session_id":  sessionID,
			"ssh_command": fmt.Sprintf("ssh %s@api.depot.dev", sandboxID),
		})
	}

	fmt.Printf("Host:     api.depot.dev\n")
	fmt.Printf("User:     %s\n", sandboxID)
	fmt.Printf("Password: Use your Depot API token ($DEPOT_TOKEN)\n")
	fmt.Println()
	fmt.Printf("Connect:  ssh %s@api.depot.dev\n", sandboxID)
	return nil
}
