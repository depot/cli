package tests

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/docker/cli/cli"
	"github.com/spf13/cobra"
)

var runShellCommandFunc = runShellCommand

type runOptions struct {
	candidateType     string
	timingsType       string
	index             int
	total             int
	splitKey          string
	command           string
	reportPaths       []string
	candidatesFile    string
	candidatesCommand string
	key               string
}

func newCmdTestsRun() *cobra.Command {
	opts := runOptions{index: -1}

	cmd := &cobra.Command{
		Use:          "run",
		Short:        "Run a test shard and upload JUnit XML reports",
		SilenceUsage: true,
		Long: `Run a test command against the candidates assigned to this shard and upload JUnit XML reports.

Candidates are optional when not splitting. When provided, they are newline-delimited runnable units read from stdin,
--candidates-file, or --candidates-command. Provide at most one source. Pass --index and --total to split candidates
across shards; candidates are required when splitting.
Depot uses historical test timings to select a balanced shard. If no timings are available for filename candidates, Depot
falls back to file-size splitting.`,
		Example: `  # Run a timing-balanced shard from stdin candidates
  go list ./... | depot tests run --index 0 --total 4 --command "xargs go test -json | go-junit-report -out reports/junit.xml" --report-path reports/junit.xml

  # Run with an explicit candidates file
  depot tests run --candidates-file tests.txt --index 0 --total 4 --command "xargs npm test -- --reporter junit --reporter-options output=reports/junit.xml" --report-path "reports/*.xml"

  # Discover candidates with a command
  depot tests run --candidates-command "go list ./..." --index 0 --total 4 --command "xargs go test -json | go-junit-report -out reports/junit.xml" --report-path reports/junit.xml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTestsRun(cmd, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.candidateType, "candidate-type", "", "Runnable candidate identity (filename, classname)")
	flags.StringVar(&opts.timingsType, "timings-type", "", "JUnit timing identity (filename, classname, testname)")
	flags.IntVar(&opts.index, "index", -1, "Zero-based shard index; pass with --total to split")
	flags.IntVar(&opts.total, "total", 0, "Total number of shards; pass with --index to split")
	flags.StringVar(&opts.splitKey, "split-key", "", "Stable test suite key for split timings (defaults to the GitHub job/action identity or default; set when running multiple suites)")
	flags.StringVar(&opts.command, "command", "", "Shell command that receives selected candidates on stdin (required)")
	flags.StringArrayVar(&opts.reportPaths, "report-path", nil, "JUnit XML report path, directory, or glob (required, repeatable)")
	flags.StringVar(&opts.candidatesFile, "candidates-file", "", "Path to newline-delimited runnable test candidates instead of stdin")
	flags.StringVar(&opts.candidatesCommand, "candidates-command", "", "Shell command that prints newline-delimited runnable test candidates")
	flags.StringVar(&opts.key, "key", "", "Report invocation key for idempotent uploads (defaults to GITHUB_ACTION or default)")

	return cmd
}

func runTestsRun(cmd *cobra.Command, opts runOptions) error {
	ctx, stop := newRunSignalContext(cmd.Context())
	defer stop()
	originalCtx := cmd.Context()
	cmd.SetContext(ctx)
	defer cmd.SetContext(originalCtx)

	if strings.TrimSpace(opts.command) == "" {
		return fmt.Errorf("--command is required")
	}

	reportPaths := splitReportPathInputs(opts.reportPaths)
	if len(reportPaths) == 0 {
		return fmt.Errorf("at least one report path is required; pass --report-path")
	}

	splitOpts := splitOptions{
		candidateType:     opts.candidateType,
		timingsType:       opts.timingsType,
		index:             opts.index,
		total:             opts.total,
		candidatesFile:    opts.candidatesFile,
		candidatesCommand: opts.candidatesCommand,
		key:               opts.splitKey,
		output:            splitOutputText,
	}
	splitOpts, err := resolveSplitOptions(cmd, splitOpts)
	if err != nil {
		return cancellationErrorOr(err)
	}
	splitRequested := runSplitRequested(splitOpts)
	mode, candidates, selectedCandidates, splitResponse, err := selectRunCandidates(cmd, splitOpts)
	if err != nil {
		return cancellationErrorOr(err)
	}

	summaryOpts := splitOpts
	if !splitRequested {
		summaryOpts.index = 0
		summaryOpts.total = 1
	}
	writeSplitSummary(cmd.ErrOrStderr(), mode, summaryOpts, len(candidates), selectedCandidates, splitResponse)
	if splitRequested && len(selectedCandidates) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "Depot skipped test command because this shard has no candidates.")
		return nil
	}

	reportBaseline, err := snapshotReportFiles(reportPaths)
	if err != nil {
		return cancellationErrorOr(err)
	}
	if err := cancellationErrorOr(ctx.Err()); err != nil {
		return err
	}

	exitCode, commandErr := runShellCommandFunc(cmd.Context(), opts.command, selectedCandidates, cmd.OutOrStdout(), cmd.ErrOrStderr())
	if commandErr != nil && errors.Is(commandErr, context.Canceled) {
		return cli.StatusError{Status: commandCanceledStatus(commandErr, exitCode), StatusCode: 1}
	}
	if err := ctx.Err(); err != nil && exitCode == 0 {
		return cancellationErrorOr(err)
	}
	reportErr := uploadAndSummarizeTestReports(cmd, reportPaths, opts.key, reportBaseline)
	if reportErr != nil && errors.Is(reportErr, context.Canceled) && exitCode == 0 {
		return cancellationErrorOr(reportErr)
	}

	if exitCode != 0 {
		status := commandStatus(commandErr, exitCode)
		if reportErr != nil {
			status = fmt.Sprintf("%s; additionally, %v", status, reportErr)
		}
		return cli.StatusError{Status: status, StatusCode: exitCode}
	}
	if commandErr != nil {
		if reportErr != nil {
			return fmt.Errorf("%w; additionally, %v", commandErr, reportErr)
		}
		return commandErr
	}
	if reportErr != nil {
		return reportErr
	}
	return nil
}

func newRunSignalContext(parent context.Context) (context.Context, func()) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

func cancellationErrorOr(err error) error {
	if err != nil && errors.Is(err, context.Canceled) {
		return cli.StatusError{Status: err.Error(), StatusCode: 1}
	}
	return err
}

func commandCanceledStatus(err error, exitCode int) string {
	if exitCode != 0 {
		return fmt.Sprintf("test command canceled after child exited with status %d: %v", exitCode, err)
	}
	return fmt.Sprintf("test command canceled: %v", err)
}

func selectRunCandidates(cmd *cobra.Command, opts splitOptions) (splitMode, []string, []string, *testresultsv1.SplitTestsResponse, error) {
	if !runSplitRequested(opts) {
		if err := validateExplicitSplitIdentityFlags(opts); err != nil {
			return "", nil, nil, nil, err
		}
		candidates, err := loadOptionalCandidates(cmd, opts.candidatesFile, opts.candidatesCommand)
		if err != nil {
			return "", nil, nil, nil, err
		}
		opts.index = 0
		opts.total = 1
		return splitModePassthrough, candidates, candidates, nil, nil
	}

	return selectTestCandidates(cmd, opts)
}

func runSplitRequested(opts splitOptions) bool {
	return opts.total > 1
}

func uploadAndSummarizeTestReports(cmd *cobra.Command, reportPaths []string, key string, baseline reportFileBaseline) error {
	filesUploaded, reportResp, err := uploadTestReportsResponse(cmd, reportOptions{
		reportPaths:             reportPaths,
		key:                     key,
		output:                  reportOutputText,
		requireUpdatedByCommand: &baseline,
	})
	if err != nil {
		return err
	}
	if err := writeReportSummary(cmd.ErrOrStderr(), filesUploaded, reportResp); err != nil {
		return fmt.Errorf("failed to write test report summary: %w", err)
	}
	return nil
}

func runShellCommand(ctx context.Context, command string, candidates []string, stdout, stderr io.Writer) (int, error) {
	shell, args := shellCommand(command)
	subCmd := exec.Command(shell, args...)
	configureShellCommand(subCmd)
	if len(candidates) == 0 {
		subCmd.Stdin = strings.NewReader("")
	} else {
		subCmd.Stdin = strings.NewReader(strings.Join(candidates, "\n") + "\n")
	}
	subCmd.Stdout = stdout
	subCmd.Stderr = stderr

	err := runCancellableShellCommand(ctx, subCmd)
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return commandExitCode(exitErr), err
	}
	return 1, err
}

func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		shell := os.Getenv("COMSPEC")
		if shell == "" {
			shell = "cmd"
		}
		return shell, []string{"/C", command}
	}

	return "/bin/sh", []string{"-c", command}
}

func commandStatus(err error, exitCode int) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("test command exited with status %d", exitCode)
}
