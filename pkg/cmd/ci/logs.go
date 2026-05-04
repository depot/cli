package ci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/briandowns/spinner"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

const (
	followAttemptRetryTimeout  = 30 * time.Second
	followAttemptRetryInterval = 1 * time.Second
	followLogIdleDelay         = 2 * time.Second
)

var ciStreamJobAttemptLogs = api.CIStreamJobAttemptLogs

func NewCmdLogs() *cobra.Command {
	var (
		orgID    string
		token    string
		job      string
		workflow string
		follow   bool
	)

	cmd := &cobra.Command{
		Use:   "logs <run-id | job-id | attempt-id>",
		Short: "Fetch logs for a CI job",
		Long: `Fetch and display log output for a CI job.

Accepts a run ID, job ID, or attempt ID. When given a run or job ID, the
command resolves to the latest attempt automatically. When starting from a run
ID, use --job and --workflow to disambiguate by workflow job key.`,
		Example: `  # Logs for a specific attempt
  depot ci logs <attempt-id>

  # Logs for the latest attempt of a job
  depot ci logs <job-id>

  # Logs for a run (auto-selects job if only one)
  depot ci logs <run-id>

  # Logs for a specific workflow job key in a run
  depot ci logs <run-id> --job test

  # Disambiguate when multiple workflows define the same job key
  depot ci logs <run-id> --job build --workflow ci.yml

  # Follow live logs for a job's latest attempt
  depot ci logs <job-id> --follow`,
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

			reporter := newLogFollowReporter(cmd.ErrOrStderr(), follow && helpers.IsTerminal())

			// First, try resolving as a run ID (or job ID — the API accepts both).
			resp, runErr := api.CIGetRunStatus(ctx, tokenVal, orgID, id)
			if runErr == nil {
				target, err := resolveLogTarget(resp, id, job, workflow)
				if follow && err != nil {
					target, err = resolveLogTargetWithFollowRetry(ctx, tokenVal, orgID, id, job, workflow, err, reporter)
				}
				if err != nil {
					return err
				}
				if target.noLogsMessage != "" {
					reporter.Message(target.noLogsMessage)
					return nil
				}

				if follow {
					if err := streamLogsWithFollowUX(ctx, tokenVal, orgID, target, cmd.OutOrStdout(), reporter); err != nil {
						return fmt.Errorf("failed to stream logs: %w", err)
					}
				} else {
					reportLogTargetSelection(target, reporter, false)
					lines, err := api.CIGetJobAttemptLogs(ctx, tokenVal, orgID, target.attemptID)
					if err != nil {
						return fmt.Errorf("failed to get logs: %w", err)
					}
					if len(lines) == 0 {
						reporter.Message(noLogsProducedMessage(target.jobKey, target.jobStatus))
						return nil
					}
					printLogLines(cmd.OutOrStdout(), lines)
				}
				return nil
			}

			// Fall back to probing the positional ID directly.
			// Don't fall back if --job or --workflow were specified — those
			// only make sense for run-level resolution.
			if job != "" || workflow != "" {
				return fmt.Errorf("failed to look up run: %w", runErr)
			}

			if follow {
				if err := streamUnresolvedLogsWithFollowUX(ctx, tokenVal, orgID, id, cmd.OutOrStdout(), reporter); err != nil {
					if unresolvedErr, ok := err.(*unresolvedLogStreamError); ok {
						return fmt.Errorf(
							"could not resolve %q as a run, job, or attempt ID:\n  as run: %v\n  as job: %v\n  as attempt: %v",
							id,
							runErr,
							unresolvedErr.jobErr,
							unresolvedErr.attemptErr,
						)
					}
					return fmt.Errorf("failed to stream logs: %w", err)
				}
			} else {
				lines, err := api.CIGetJobAttemptLogs(ctx, tokenVal, orgID, id)
				if err != nil {
					// Both paths failed — show both errors so the user can
					// distinguish "bad ID" from "auth/network failure".
					return fmt.Errorf(
						"could not resolve %q as a run, job, or attempt ID:\n  as run/job: %v\n  as attempt: %v",
						id,
						runErr,
						err,
					)
				}
				if len(lines) == 0 {
					reporter.Message("No logs were produced.")
					return nil
				}
				printLogLines(cmd.OutOrStdout(), lines)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&job, "job", "", "Workflow job key to select when using a run ID")
	cmd.Flags().StringVar(&workflow, "workflow", "", "Workflow path to filter jobs (e.g. ci.yml)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow live logs")

	return cmd
}

func printLogLines(w io.Writer, lines []*civ1.LogLine) {
	for _, line := range lines {
		fmt.Fprintln(w, line.Body)
	}
}

type logTarget struct {
	attemptID      string
	attemptNumber  int32
	attemptStatus  string
	jobID          string
	streamJobID    string
	jobKey         string
	jobStatus      string
	workflowPath   string
	noLogsMessage  string
	hasAlternates  bool
	alternateLabel string
}

type pendingLogTargetError struct {
	message        string
	timeoutMessage string
}

func (e *pendingLogTargetError) Error() string {
	return e.message
}

func (e *pendingLogTargetError) TimeoutError() error {
	if e.timeoutMessage != "" {
		return errors.New(e.timeoutMessage)
	}
	return fmt.Errorf("timed out while %s", strings.TrimSuffix(strings.ToLower(e.message), "..."))
}

type logFollowReporter struct {
	w           io.Writer
	interactive bool
	spinner     *spinner.Spinner
	lastStatus  string
	mu          sync.Mutex
	idleTimer   *time.Timer
	idleDelay   time.Duration
}

func newLogFollowReporter(w io.Writer, interactive bool) *logFollowReporter {
	return &logFollowReporter{w: w, interactive: interactive, idleDelay: followLogIdleDelay}
}

func (r *logFollowReporter) Status(message string) {
	if r == nil || message == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if message == r.lastStatus && (!r.interactive || r.spinner != nil) {
		return
	}
	r.lastStatus = message

	if !r.interactive {
		fmt.Fprintln(r.w, message)
		return
	}

	r.stopLocked()
	r.startLocked(message)
}

func (r *logFollowReporter) Message(message string) {
	if r == nil || message == "" {
		return
	}
	r.Stop()
	fmt.Fprintln(r.w, message)
}

func (r *logFollowReporter) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopLocked()
	r.lastStatus = ""
}

func (r *logFollowReporter) pause() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopLocked()
}

func (r *logFollowReporter) SawLogs() {
	if r == nil || !r.interactive {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.stopLocked()
	if r.lastStatus == "" {
		return
	}
	r.idleTimer = time.AfterFunc(r.idleDelay, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.spinner == nil && r.lastStatus != "" {
			r.startLocked(r.lastStatus)
		}
	})
}

func (r *logFollowReporter) startLocked(message string) {
	r.spinner = spinner.New(spinner.CharSets[14], 100*time.Millisecond, spinner.WithWriter(r.w))
	r.spinner.Prefix = message + " "
	r.spinner.Start()
}

func (r *logFollowReporter) stopLocked() {
	if r.idleTimer != nil {
		r.idleTimer.Stop()
		r.idleTimer = nil
	}
	if r.spinner == nil {
		return
	}
	r.spinner.Stop()
	r.spinner = nil
}

type followLogWriter struct {
	w        io.Writer
	reporter *logFollowReporter
	lines    int
}

func (w *followLogWriter) Write(p []byte) (int, error) {
	w.lines++
	w.reporter.pause()
	n, err := w.w.Write(p)
	if err == nil {
		w.reporter.SawLogs()
	}
	return n, err
}

func streamLogsWithFollowUX(
	ctx context.Context,
	tokenVal string,
	orgID string,
	target logTarget,
	out io.Writer,
	reporter *logFollowReporter,
) error {
	reportLogTargetSelection(target, reporter, true)
	streamTarget := api.CILogStreamTarget{AttemptID: target.attemptID}
	if target.streamJobID != "" {
		streamTarget = api.CILogStreamTarget{JobID: target.streamJobID}
	}

	return streamLogTargetWithFollowUX(ctx, tokenVal, orgID, streamTarget, target, out, reporter)
}

type unresolvedLogStreamError struct {
	jobErr     error
	attemptErr error
}

func (e *unresolvedLogStreamError) Error() string {
	return fmt.Sprintf("as job: %v; as attempt: %v", e.jobErr, e.attemptErr)
}

func streamUnresolvedLogsWithFollowUX(
	ctx context.Context,
	tokenVal string,
	orgID string,
	id string,
	out io.Writer,
	reporter *logFollowReporter,
) error {
	reporter.Message(fmt.Sprintf("Following logs for %s.", id))

	jobErr := streamLogTargetWithFollowUX(
		ctx,
		tokenVal,
		orgID,
		api.CILogStreamTarget{JobID: id},
		logTarget{},
		out,
		reporter,
	)
	if jobErr == nil {
		return nil
	}
	if isContextDoneError(jobErr) {
		return jobErr
	}

	attemptErr := streamLogTargetWithFollowUX(
		ctx,
		tokenVal,
		orgID,
		api.CILogStreamTarget{AttemptID: id},
		logTarget{},
		out,
		reporter,
	)
	if attemptErr == nil {
		return nil
	}
	if isContextDoneError(attemptErr) {
		return attemptErr
	}

	return &unresolvedLogStreamError{jobErr: jobErr, attemptErr: attemptErr}
}

func isContextDoneError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func streamLogTargetWithFollowUX(
	ctx context.Context,
	tokenVal string,
	orgID string,
	streamTarget api.CILogStreamTarget,
	target logTarget,
	out io.Writer,
	reporter *logFollowReporter,
) error {
	reporter.Status(logStreamWaitingMessage(target))

	logWriter := &followLogWriter{w: out, reporter: reporter}
	err := ciStreamJobAttemptLogs(ctx, tokenVal, orgID, streamTarget, logWriter, func(status string) {
		if logWriter.lines > 0 && status == target.attemptStatus {
			return
		}
		target.attemptStatus = status
		reporter.Status(logStreamWaitingMessage(target))
	})
	reporter.Stop()
	if err != nil {
		return err
	}

	if logWriter.lines == 0 {
		reporter.Message(noStreamLogsReceivedMessage(target))
		return nil
	}

	return nil
}

func resolveLogTargetWithFollowRetry(
	ctx context.Context,
	tokenVal string,
	orgID string,
	id string,
	job string,
	workflow string,
	initialErr error,
	reporter *logFollowReporter,
) (logTarget, error) {
	if !isFollowRetryableResolutionError(initialErr) {
		return logTarget{}, initialErr
	}

	timeout := time.NewTimer(followAttemptRetryTimeout)
	defer timeout.Stop()
	ticker := time.NewTicker(followAttemptRetryInterval)
	defer ticker.Stop()

	lastErr := initialErr
	reportPendingLogTarget(lastErr, reporter)
	for {
		select {
		case <-ctx.Done():
			return logTarget{}, ctx.Err()
		case <-timeout.C:
			if pending, ok := lastErr.(*pendingLogTargetError); ok {
				return logTarget{}, pending.TimeoutError()
			}
			return logTarget{}, lastErr
		case <-ticker.C:
			var err error
			resp, err := api.CIGetRunStatus(ctx, tokenVal, orgID, id)
			if err != nil {
				return logTarget{}, err
			}
			target, err := resolveLogTarget(resp, id, job, workflow)
			if err == nil {
				return target, nil
			}
			if !isFollowRetryableResolutionError(err) {
				return logTarget{}, err
			}
			lastErr = err
			reportPendingLogTarget(lastErr, reporter)
		}
	}
}

func isFollowRetryableResolutionError(err error) bool {
	_, ok := err.(*pendingLogTargetError)
	return ok
}

func reportPendingLogTarget(err error, reporter *logFollowReporter) {
	if pending, ok := err.(*pendingLogTargetError); ok {
		reporter.Status(pending.message)
	}
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

func resolveLogTarget(resp *civ1.GetRunStatusResponse, originalID, jobKey, workflowFilter string) (logTarget, error) {
	targetJob, workflowPath, err := findLogsJob(resp, originalID, jobKey, workflowFilter)
	if err != nil {
		if isRetryableLogJobError(err) {
			if isActiveRunStatus(resp.Status) {
				return logTarget{}, &pendingLogTargetError{
					message:        pendingRunLogsMessage(resp.Status),
					timeoutMessage: "Timed out waiting for jobs to be created. Try again after the run starts.",
				}
			}
			return logTarget{noLogsMessage: terminalRunNoLogsMessage(resp.Status, err)}, nil
		}
		return logTarget{}, err
	}

	if len(targetJob.Attempts) == 0 {
		if isActiveJobStatus(targetJob.Status) {
			return logTarget{}, &pendingLogTargetError{
				message:        pendingJobLogsMessage(targetJob),
				timeoutMessage: fmt.Sprintf("Timed out waiting for job %q to start. Try again after the job is running.", jobKeyShort(targetJob.JobKey)),
			}
		}
		return logTarget{
			jobKey:        targetJob.JobKey,
			jobID:         targetJob.JobId,
			jobStatus:     targetJob.Status,
			workflowPath:  workflowPath,
			noLogsMessage: noLogsProducedMessage(targetJob.JobKey, targetJob.Status),
		}, nil
	}

	latest := targetJob.Attempts[0]
	for _, a := range targetJob.Attempts[1:] {
		if a.Attempt > latest.Attempt {
			latest = a
		}
	}

	var alternateLabel string
	if len(targetJob.Attempts) > 1 {
		var others []string
		for _, a := range targetJob.Attempts {
			if a.AttemptId != latest.AttemptId {
				others = append(others, fmt.Sprintf("#%d %s", a.Attempt, a.AttemptId))
			}
		}
		alternateLabel = strings.Join(others, ", ")
	}

	return logTarget{
		attemptID:      latest.AttemptId,
		attemptNumber:  latest.Attempt,
		attemptStatus:  latest.Status,
		jobID:          targetJob.JobId,
		streamJobID:    directJobIDStreamTarget(originalID, targetJob),
		jobKey:         targetJob.JobKey,
		jobStatus:      targetJob.Status,
		workflowPath:   workflowPath,
		hasAlternates:  len(targetJob.Attempts) > 1,
		alternateLabel: alternateLabel,
	}, nil
}

func directJobIDStreamTarget(originalID string, job *civ1.JobStatus) string {
	if job != nil && originalID == job.JobId {
		return job.JobId
	}
	return ""
}

func isRetryableLogJobError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "has no jobs") ||
		strings.Contains(message, "no jobs found in workflow")
}

func isActiveRunStatus(status string) bool {
	switch status {
	case "queued", "running":
		return true
	default:
		return false
	}
}

func isActiveJobStatus(status string) bool {
	switch status {
	case "queued", "waiting", "running":
		return true
	default:
		return false
	}
}

func isTerminalStatus(status string) bool {
	return status == "finished" || status == "failed" || status == "cancelled" || status == "skipped"
}

func pendingRunLogsMessage(status string) string {
	if status == "" {
		return "Waiting for jobs to be created..."
	}
	return fmt.Sprintf("Waiting for jobs to be created (run status: %s)...", status)
}

func pendingJobLogsMessage(job *civ1.JobStatus) string {
	return fmt.Sprintf("Waiting for job %q to start (status: %s)...", jobKeyShort(job.JobKey), job.Status)
}

func terminalRunNoLogsMessage(status string, err error) string {
	if status == "" {
		return fmt.Sprintf("%s; no logs were produced.", err.Error())
	}
	return fmt.Sprintf("%s (run status: %s); no logs were produced.", err.Error(), status)
}

func noLogsProducedMessage(jobKey, status string) string {
	if jobKey == "" {
		return "No logs were produced."
	}
	name := jobKeyShort(jobKey)
	switch {
	case status == "skipped":
		return fmt.Sprintf("Job %q was skipped; no logs were produced.", name)
	case isTerminalStatus(status):
		return fmt.Sprintf("Job %q is %s; no logs were produced.", name, status)
	case status != "":
		return fmt.Sprintf("No logs were produced yet for job %q (status: %s).", name, status)
	default:
		return "No logs were produced."
	}
}

func noStreamLogsReceivedMessage(target logTarget) string {
	status := logTargetStatus(target)
	if target.jobKey != "" {
		if status != "" {
			return fmt.Sprintf("Log stream ended for job %q (status: %s); no logs were produced.", jobKeyShort(target.jobKey), status)
		}
		return fmt.Sprintf("Log stream ended for job %q; no logs were produced.", jobKeyShort(target.jobKey))
	}
	if target.attemptID != "" {
		if status != "" {
			return fmt.Sprintf("Log stream ended for attempt %s (status: %s); no logs were produced.", target.attemptID, status)
		}
		return fmt.Sprintf("Log stream ended for attempt %s; no logs were produced.", target.attemptID)
	}
	if status != "" {
		return fmt.Sprintf("Log stream ended (status: %s); no logs were produced.", status)
	}
	return "Log stream ended; no logs were produced."
}

func logStreamWaitingMessage(target logTarget) string {
	status := logTargetStatus(target)
	if target.jobKey != "" {
		if status != "" {
			return fmt.Sprintf("Waiting for logs from job %q (status: %s)...", jobKeyShort(target.jobKey), status)
		}
		return fmt.Sprintf("Waiting for logs from job %q...", jobKeyShort(target.jobKey))
	}
	if target.attemptID != "" {
		if status != "" {
			return fmt.Sprintf("Waiting for logs from attempt %s (status: %s)...", target.attemptID, status)
		}
		return fmt.Sprintf("Waiting for logs from attempt %s...", target.attemptID)
	}
	if status != "" {
		return fmt.Sprintf("Waiting for logs (status: %s)...", status)
	}
	return "Waiting for logs..."
}

func logTargetStatus(target logTarget) string {
	if target.attemptStatus != "" {
		return target.attemptStatus
	}
	return target.jobStatus
}

func reportLogTargetSelection(target logTarget, reporter *logFollowReporter, follow bool) {
	if target.attemptID == "" {
		return
	}

	action := "Fetching"
	if follow {
		action = "Following"
	}

	if target.jobKey == "" {
		reporter.Message(fmt.Sprintf("%s logs for attempt %s.", action, target.attemptID))
		return
	}

	parts := []string{fmt.Sprintf("%s logs for job %q", action, jobKeyShort(target.jobKey))}
	if target.workflowPath != "" {
		parts = append(parts, fmt.Sprintf("from %s", target.workflowPath))
	}
	if target.attemptNumber > 0 {
		parts = append(parts, fmt.Sprintf("attempt #%d", target.attemptNumber))
	}
	if target.attemptStatus != "" {
		parts = append(parts, fmt.Sprintf("status: %s", target.attemptStatus))
	}
	if target.hasAlternates && target.alternateLabel != "" {
		parts = append(parts, fmt.Sprintf("other attempts: %s", target.alternateLabel))
	}
	reporter.Message(strings.Join(parts, ", ") + ".")
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

	// Match by workflow job key (--job flag): exact > suffix > segment, best tier wins.
	if jobKey != "" {
		bestTier := 0
		tierMatches := map[int][]jobCandidate{}
		for _, c := range candidates {
			if tier := matchJobKey(c.job.JobKey, jobKey); tier > 0 {
				tierMatches[tier] = append(tierMatches[tier], c)
				if bestTier == 0 || tier < bestTier {
					bestTier = tier
				}
			}
		}

		matches := tierMatches[bestTier]

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
			// Check if ambiguity is cross-workflow or within a single workflow.
			uniquePaths := map[string]struct{}{}
			for _, m := range matches {
				uniquePaths[m.workflowPath] = struct{}{}
			}
			if len(uniquePaths) > 1 {
				paths := make([]string, 0, len(uniquePaths))
				for path := range uniquePaths {
					paths = append(paths, path)
				}
				return nil, "", fmt.Errorf("job %q exists in multiple workflows, specify one with --workflow: %s", jobKey, strings.Join(paths, ", "))
			}
			keys := make([]string, len(matches))
			for i, m := range matches {
				keys[i] = displayNames[m.job.JobKey]
			}
			return nil, "", fmt.Errorf("job %q matches multiple jobs, use a more specific --job value: %s", jobKey, strings.Join(keys, ", "))
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
