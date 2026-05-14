package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
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
	logOutputText              = "text"
	logOutputJSON              = "json"
)

var (
	ciGetRunStatus               = api.CIGetRunStatus
	ciGetJobAttemptLogs          = api.CIGetJobAttemptLogs
	ciGetJobAttemptLogsForTarget = api.CIGetJobAttemptLogsForTarget
	ciExportJobAttemptLogs       = api.CIExportJobAttemptLogs
	ciStreamJobAttemptLogs       = api.CIStreamJobAttemptLogs
	ciStreamJobAttemptLogLines   = api.CIStreamJobAttemptLogLines
)

func NewCmdLogs() *cobra.Command {
	var (
		orgID      string
		token      string
		job        string
		workflow   string
		follow     bool
		output     string
		outputFile string
		timestamps bool
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
  depot ci logs <job-id> --follow

  # Prefix log lines with persisted UTC timestamps
  depot ci logs <attempt-id> --timestamps

  # Emit newline-delimited JSON log events
  depot ci logs <attempt-id> --output json

  # Download a timestamped text export to a file
  depot ci logs <attempt-id> --output-file logs.txt

  # Download a JSONL export to a file
  depot ci logs <attempt-id> --output json --output-file logs.jsonl`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}

			outputOptions := logOutputOptions{timestamps: timestamps, output: output}
			if err := outputOptions.validate(); err != nil {
				return err
			}
			if follow && outputFile != "" {
				return fmt.Errorf("--follow cannot be used with --output-file")
			}
			if outputFile == "-" {
				return fmt.Errorf("--output-file - is not supported; omit --output-file to write logs to stdout, or provide a file path")
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

			reporterWriter := cmd.ErrOrStderr()
			reporterInteractive := follow && helpers.IsTerminal()
			if outputOptions.json() {
				reporterWriter = io.Discard
				reporterInteractive = false
			}
			if outputFile != "" {
				reporterWriter = io.Discard
				reporterInteractive = false
			}
			reporter := newLogFollowReporter(reporterWriter, reporterInteractive)

			// First, try resolving as a run ID (or job ID — the API accepts both).
			resolutionOptions := logTargetResolutionOptionsForOutput(outputOptions, outputFile)
			resp, runErr := ciGetRunStatus(ctx, tokenVal, orgID, id)
			if runErr == nil {
				target, err := resolveLogTargetWithOptions(resp, id, job, workflow, resolutionOptions)
				if follow && err != nil {
					target, err = resolveLogTargetWithFollowRetry(ctx, tokenVal, orgID, id, job, workflow, err, reporter, resolutionOptions)
				}
				if err != nil {
					return err
				}
				if target.noLogsMessage != "" {
					if outputFile != "" {
						fmt.Fprintln(cmd.ErrOrStderr(), target.noLogsMessage)
					} else {
						reporter.Message(target.noLogsMessage)
					}
					return nil
				}

				if outputFile != "" {
					return downloadLogsToFile(ctx, tokenVal, orgID, api.CILogStreamTarget{AttemptID: target.attemptID}, outputOptions, outputFile, cmd.ErrOrStderr())
				} else if follow {
					if err := streamLogsWithFollowUX(ctx, tokenVal, orgID, target, cmd.OutOrStdout(), reporter, outputOptions); err != nil {
						return fmt.Errorf("failed to stream logs: %w", err)
					}
				} else {
					reportLogTargetSelection(target, reporter, false)
					lines, err := ciGetJobAttemptLogs(ctx, tokenVal, orgID, target.attemptID)
					if err != nil {
						return fmt.Errorf("failed to get logs: %w", err)
					}
					if len(lines) == 0 {
						reporter.Message(noLogsProducedMessage(target.jobKey, target.jobStatus))
						return nil
					}
					if err := printLogLines(cmd.OutOrStdout(), lines, outputOptions); err != nil {
						return err
					}
				}
				return nil
			}

			// Fall back to probing the positional ID directly.
			// Don't fall back if --job or --workflow were specified — those
			// only make sense for run-level resolution.
			if job != "" || workflow != "" {
				return fmt.Errorf("failed to look up run: %w", runErr)
			}

			if outputFile != "" {
				if err := downloadUnresolvedLogsToFile(ctx, tokenVal, orgID, id, outputOptions, outputFile, cmd.ErrOrStderr()); err != nil {
					return fmt.Errorf(
						"could not resolve %q as a run, job, or attempt ID:\n  as run/job: %v\n  as job/attempt export: %v",
						id,
						runErr,
						err,
					)
				}
			} else if follow {
				if err := streamUnresolvedLogsWithFollowUX(ctx, tokenVal, orgID, id, cmd.OutOrStdout(), reporter, outputOptions); err != nil {
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
				lines, err := getUnresolvedHistoricalLogs(ctx, tokenVal, orgID, id)
				if err != nil {
					if unresolvedErr, ok := err.(*unresolvedHistoricalLogsError); ok {
						return fmt.Errorf(
							"could not resolve %q as a run, job, or attempt ID:\n  as run/job: %v\n  as attempt: %v\n  as job: %v",
							id,
							runErr,
							unresolvedErr.attemptErr,
							unresolvedErr.jobErr,
						)
					}

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
				if err := printLogLines(cmd.OutOrStdout(), lines, outputOptions); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&job, "job", "", "Workflow job key to select when using a run ID")
	cmd.Flags().StringVar(&workflow, "workflow", "", "Workflow path to filter jobs (e.g. ci.yml)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow live logs")
	cmd.Flags().BoolVar(&timestamps, "timestamps", false, "Prefix plain log lines with UTC timestamps")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (text, json)")
	cmd.Flags().StringVar(&outputFile, "output-file", "", "Write a finite log export to the provided file path")

	return cmd
}

type logOutputOptions struct {
	timestamps bool
	output     string
}

func (o logOutputOptions) validate() error {
	switch o.output {
	case "", logOutputText, logOutputJSON:
		return nil
	default:
		return fmt.Errorf("unsupported output %q (valid: text, json)", o.output)
	}
}

func (o logOutputOptions) json() bool {
	return o.output == logOutputJSON
}

func (o logOutputOptions) exportFormat() civ1.JobAttemptLogExportFormat {
	if o.json() {
		return civ1.JobAttemptLogExportFormat_JOB_ATTEMPT_LOG_EXPORT_FORMAT_JSONL
	}
	return civ1.JobAttemptLogExportFormat_JOB_ATTEMPT_LOG_EXPORT_FORMAT_TEXT
}

type logLineEvent struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	TimestampMs int64  `json:"timestamp_ms"`
	Stream      string `json:"stream"`
	StepKey     string `json:"step_key"`
	StepID      string `json:"step_id"`
	StepName    string `json:"step_name"`
	LineNumber  uint32 `json:"line_number"`
	Body        string `json:"body"`
}

type logStatusEvent struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type logEndEvent struct {
	Type      string `json:"type"`
	Status    string `json:"status,omitempty"`
	LineCount int    `json:"line_count"`
}

type logJSONEventWriter struct {
	enc *json.Encoder
}

func newLogJSONEventWriter(w io.Writer) *logJSONEventWriter {
	return &logJSONEventWriter{enc: json.NewEncoder(w)}
}

func (w *logJSONEventWriter) Line(line *civ1.LogLine) error {
	return w.enc.Encode(logLineEventFromLine(line))
}

func (w *logJSONEventWriter) Status(status string) error {
	if status == "" {
		return nil
	}
	return w.enc.Encode(logStatusEvent{Type: "status", Status: status})
}

func (w *logJSONEventWriter) End(status string, lineCount int) error {
	return w.enc.Encode(logEndEvent{Type: "end", Status: status, LineCount: lineCount})
}

func logLineEventFromLine(line *civ1.LogLine) logLineEvent {
	return logLineEvent{
		Type:        "line",
		Timestamp:   formatLogTimestamp(line),
		TimestampMs: line.GetTimestampMs(),
		Stream:      logStreamName(line.GetStream()),
		StepKey:     line.GetStepKey(),
		StepID:      line.GetStepId(),
		StepName:    line.GetStepName(),
		LineNumber:  line.GetLineNumber(),
		Body:        line.GetBody(),
	}
}

func printLogLines(w io.Writer, lines []*civ1.LogLine, options logOutputOptions) error {
	if options.json() {
		writer := newLogJSONEventWriter(w)
		for _, line := range lines {
			if err := writer.Line(line); err != nil {
				return err
			}
		}
		return nil
	}

	for _, line := range lines {
		if err := writePlainLogLine(w, line, options.timestamps); err != nil {
			return err
		}
	}
	return nil
}

func writePlainLogLine(w io.Writer, line *civ1.LogLine, timestamps bool) error {
	text := line.GetBody() + "\n"
	if timestamps {
		text = formatLogTimestamp(line) + " " + line.GetBody() + "\n"
	}
	n, err := io.WriteString(w, text)
	if err != nil {
		return err
	}
	if n != len(text) {
		return io.ErrShortWrite
	}
	return nil
}

func formatLogTimestamp(line *civ1.LogLine) string {
	return time.UnixMilli(line.GetTimestampMs()).UTC().Format(time.RFC3339Nano)
}

func logStreamName(stream uint32) string {
	switch stream {
	case 0:
		return "stdout"
	case 1:
		return "stderr"
	default:
		return fmt.Sprintf("stream_%d", stream)
	}
}

func downloadLogsToFile(ctx context.Context, tokenVal string, orgID string, target api.CILogStreamTarget, options logOutputOptions, outputFile string, statusWriter io.Writer) error {
	return runLogFileDownload(outputFile, statusWriter, func() error {
		return exportLogsToFile(ctx, tokenVal, orgID, target, options, outputFile)
	})
}

func downloadUnresolvedLogsToFile(ctx context.Context, tokenVal string, orgID string, id string, options logOutputOptions, outputFile string, statusWriter io.Writer) error {
	return runLogFileDownload(outputFile, statusWriter, func() error {
		return exportUnresolvedLogsToFile(ctx, tokenVal, orgID, id, options, outputFile)
	})
}

func runLogFileDownload(outputFile string, statusWriter io.Writer, export func() error) error {
	if statusWriter != nil {
		fmt.Fprintf(statusWriter, "Downloading logs to %s...\n", outputFile)
	}
	if err := export(); err != nil {
		return err
	}

	info, err := os.Stat(outputFile)
	if err != nil {
		return err
	}
	if statusWriter != nil {
		fmt.Fprintf(statusWriter, "Downloaded logs to %s (%s).\n", outputFile, logDownloadByteCount(info.Size()))
	}
	return nil
}

func logDownloadByteCount(size int64) string {
	if size == 1 {
		return "1 byte"
	}
	return fmt.Sprintf("%d bytes", size)
}

func exportLogsToFile(ctx context.Context, tokenVal string, orgID string, target api.CILogStreamTarget, options logOutputOptions, outputFile string) error {
	dir := filepath.Dir(outputFile)
	if dir == "" {
		dir = "."
	}
	base := filepath.Base(outputFile)
	temp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	completed := false
	defer func() {
		if !completed {
			_ = os.Remove(tempPath)
		}
	}()

	if _, err := ciExportJobAttemptLogs(ctx, tokenVal, orgID, target, options.exportFormat(), temp); err != nil {
		_ = temp.Close()
		return fmt.Errorf("failed to export logs: %w", err)
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, outputFile); err != nil {
		return err
	}
	completed = true
	return nil
}

func exportUnresolvedLogsToFile(ctx context.Context, tokenVal string, orgID string, id string, options logOutputOptions, outputFile string) error {
	jobErr := exportLogsToFile(ctx, tokenVal, orgID, api.CILogStreamTarget{JobID: id}, options, outputFile)
	if jobErr == nil || isContextDoneError(jobErr) {
		return jobErr
	}

	attemptErr := exportLogsToFile(ctx, tokenVal, orgID, api.CILogStreamTarget{AttemptID: id}, options, outputFile)
	if attemptErr == nil || isContextDoneError(attemptErr) {
		return attemptErr
	}

	return fmt.Errorf("as job: %v; as attempt: %v", jobErr, attemptErr)
}

type unresolvedHistoricalLogsError struct {
	attemptErr error
	jobErr     error
}

func (e *unresolvedHistoricalLogsError) Error() string {
	return fmt.Sprintf("as attempt: %v; as job: %v", e.attemptErr, e.jobErr)
}

func getUnresolvedHistoricalLogs(ctx context.Context, tokenVal string, orgID string, id string) ([]*civ1.LogLine, error) {
	lines, attemptErr := ciGetJobAttemptLogs(ctx, tokenVal, orgID, id)
	if attemptErr == nil || isContextDoneError(attemptErr) {
		return lines, attemptErr
	}
	if connect.CodeOf(attemptErr) != connect.CodeNotFound {
		return nil, attemptErr
	}

	lines, jobErr := ciGetJobAttemptLogsForTarget(ctx, tokenVal, orgID, api.CILogStreamTarget{JobID: id})
	if jobErr == nil || isContextDoneError(jobErr) {
		return lines, jobErr
	}

	return nil, &unresolvedHistoricalLogsError{attemptErr: attemptErr, jobErr: jobErr}
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

func (w *followLogWriter) WriteLine(line *civ1.LogLine, timestamps bool) error {
	w.lines++
	w.reporter.pause()
	err := writePlainLogLine(w.w, line, timestamps)
	if err == nil {
		w.reporter.SawLogs()
	}
	return err
}

func streamLogsWithFollowUX(
	ctx context.Context,
	tokenVal string,
	orgID string,
	target logTarget,
	out io.Writer,
	reporter *logFollowReporter,
	options logOutputOptions,
) error {
	if !options.json() {
		reportLogTargetSelection(target, reporter, true)
	}
	streamTarget := api.CILogStreamTarget{AttemptID: target.attemptID}
	if target.streamJobID != "" {
		streamTarget = api.CILogStreamTarget{JobID: target.streamJobID}
	}

	return streamLogTargetWithFollowUX(ctx, tokenVal, orgID, streamTarget, target, out, reporter, options)
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
	options logOutputOptions,
) error {
	if !options.json() {
		reporter.Message(fmt.Sprintf("Following logs for %s.", id))
	}

	jobErr := streamLogTargetWithFollowUX(
		ctx,
		tokenVal,
		orgID,
		api.CILogStreamTarget{JobID: id},
		logTarget{},
		out,
		reporter,
		options,
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
		options,
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
	options logOutputOptions,
) error {
	if options.json() {
		return streamLogTargetAsJSON(ctx, tokenVal, orgID, streamTarget, target, out)
	}

	reporter.Status(logStreamWaitingMessage(target))

	logWriter := &followLogWriter{w: out, reporter: reporter}
	var err error
	if options.timestamps {
		err = ciStreamJobAttemptLogLines(ctx, tokenVal, orgID, streamTarget, func(line *civ1.LogLine) error {
			return logWriter.WriteLine(line, true)
		}, func(status string) error {
			if logWriter.lines > 0 && status == target.attemptStatus {
				return nil
			}
			target.attemptStatus = status
			reporter.Status(logStreamWaitingMessage(target))
			return nil
		})
	} else {
		err = ciStreamJobAttemptLogs(ctx, tokenVal, orgID, streamTarget, logWriter, func(status string) {
			if logWriter.lines > 0 && status == target.attemptStatus {
				return
			}
			target.attemptStatus = status
			reporter.Status(logStreamWaitingMessage(target))
		})
	}
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

func streamLogTargetAsJSON(
	ctx context.Context,
	tokenVal string,
	orgID string,
	streamTarget api.CILogStreamTarget,
	target logTarget,
	out io.Writer,
) error {
	writer := newLogJSONEventWriter(out)
	lineCount := 0
	lastStatus := ""

	if status := logTargetStatus(target); status != "" {
		if err := writer.Status(status); err != nil {
			return err
		}
		lastStatus = status
	}

	err := ciStreamJobAttemptLogLines(ctx, tokenVal, orgID, streamTarget, func(line *civ1.LogLine) error {
		lineCount++
		return writer.Line(line)
	}, func(status string) error {
		target.attemptStatus = status
		if status == "" || status == lastStatus {
			return nil
		}
		lastStatus = status
		return writer.Status(status)
	})
	if err != nil {
		return err
	}

	return writer.End(logTargetStatus(target), lineCount)
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
	options logTargetResolutionOptions,
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
	defer reporter.Stop()
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
			resp, err := ciGetRunStatus(ctx, tokenVal, orgID, id)
			if err != nil {
				return logTarget{}, err
			}
			target, err := resolveLogTargetWithOptions(resp, id, job, workflow, options)
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

type logTargetResolutionOptions struct {
	allowInteractive bool
}

func logTargetResolutionOptionsForOutput(outputOptions logOutputOptions, outputFile string) logTargetResolutionOptions {
	return logTargetResolutionOptions{allowInteractive: !outputOptions.json() && outputFile == ""}
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
	return resolveLogTargetWithOptions(resp, originalID, jobKey, workflowFilter, logTargetResolutionOptions{allowInteractive: true})
}

func resolveLogTargetWithOptions(resp *civ1.GetRunStatusResponse, originalID, jobKey, workflowFilter string, options logTargetResolutionOptions) (logTarget, error) {
	targetJob, workflowPath, err := findLogsJobWithOptions(resp, originalID, jobKey, workflowFilter, options)
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
	return findLogsJobWithOptions(resp, originalID, jobKey, workflowFilter, logTargetResolutionOptions{allowInteractive: true})
}

func findLogsJobWithOptions(resp *civ1.GetRunStatusResponse, originalID, jobKey, workflowFilter string, options logTargetResolutionOptions) (*civ1.JobStatus, string, error) {
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

	// Interactive fuzzy picker when terminal is available and interactive mode is allowed.
	if options.allowInteractive && helpers.IsTerminal() {
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
