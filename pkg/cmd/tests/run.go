package tests

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/depot/cli/pkg/api"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/docker/cli/cli"
	"github.com/spf13/cobra"
)

var (
	splitTestsFunc            = api.SplitTests
	reportTestResultsFunc     = api.ReportTestResults
	resolveOIDCCredentialFunc = resolveOIDCCredential
	runShellCommandFunc       = runShellCommand
	workingDirectoryFunc      = os.Getwd
)

const (
	splitTestsRequestTimeout        = 2 * time.Minute
	reportTestResultsRequestTimeout = 10 * time.Minute
)

type runOptions struct {
	splitBy       string
	candidateType string
	timingsType   string
	index         int
	total         int
	command       string
	reportPath    string
	candidates    string
	key           string
	output        string
}

func newCmdTestsRun() *cobra.Command {
	opts := runOptions{splitBy: string(splitModeTimings), index: -1}

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a timing-balanced test shard",
		Long: `Run a test command against the candidates assigned to this shard and upload JUnit XML reports.

Candidates are newline-delimited runnable units read from stdin or --candidates. By default, Depot uses historical test
timings to select a balanced shard. Use --split-by name or --split-by filesize for local deterministic splitting.`,
		Example: `  # Run a timing-balanced shard from stdin candidates
  go list ./... | depot tests run --index 0 --total 4 --command "xargs gotestsum --junitfile junit.xml --format testname --" --report-path junit.xml --candidate-type classname

  # Run with an explicit candidates file
  depot tests run --candidates tests.txt --index 0 --total 4 --command "xargs npm test --" --report-path reports/*.xml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTestsRun(cmd, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.splitBy, "split-by", string(splitModeTimings), "Split mode (timings, name, filesize)")
	flags.StringVar(&opts.candidateType, "candidate-type", "", "Runnable candidate identity (filename, classname)")
	flags.StringVar(&opts.timingsType, "timings-type", "", "JUnit timing identity (filename, classname, testname)")
	flags.IntVar(&opts.index, "index", -1, "Zero-based shard index")
	flags.IntVar(&opts.total, "total", 0, "Total number of shards")
	flags.StringVar(&opts.command, "command", "", "Shell command that receives selected candidates on stdin")
	flags.StringVar(&opts.reportPath, "report-path", "", "JUnit XML report path, directory, or glob")
	flags.StringVar(&opts.candidates, "candidates", "", "Path to newline-delimited runnable test candidates instead of stdin")
	flags.StringVar(&opts.key, "key", "", "Test invocation key")

	return cmd
}

func runTestsRun(cmd *cobra.Command, opts runOptions) error {
	if opts.command == "" {
		return fmt.Errorf("--command is required")
	}
	if opts.reportPath == "" {
		return fmt.Errorf("--report-path is required")
	}

	mode, candidates, selectedCandidates, splitResponse, err := selectTestCandidates(cmd, opts)
	if err != nil {
		return err
	}

	writeSplitSummary(cmd.ErrOrStderr(), mode, opts, len(candidates), selectedCandidates, splitResponse)

	exitCode, commandErr := runShellCommandFunc(cmd.Context(), opts.command, selectedCandidates, cmd.OutOrStdout(), cmd.ErrOrStderr())
	reportErr := uploadTestReports(cmd.Context(), opts)

	if exitCode != 0 {
		status := commandStatus(commandErr, exitCode)
		if reportErr != nil {
			status = fmt.Sprintf("%s; additionally, failed to upload test reports: %v", status, reportErr)
		}
		return cli.StatusError{Status: status, StatusCode: exitCode}
	}
	if commandErr != nil {
		if reportErr != nil {
			return fmt.Errorf("%w; additionally, failed to upload test reports: %v", commandErr, reportErr)
		}
		return commandErr
	}
	if reportErr != nil {
		return fmt.Errorf("failed to upload test reports: %w", reportErr)
	}
	return nil
}

func selectTestCandidates(cmd *cobra.Command, opts runOptions) (splitMode, []string, []string, *testresultsv1.SplitTestsResponse, error) {
	mode, err := parseSplitMode(opts.splitBy)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if err := validateShard(opts.index, opts.total); err != nil {
		return "", nil, nil, nil, err
	}
	if err := validateExplicitRunIdentityFlags(opts); err != nil {
		return "", nil, nil, nil, err
	}
	if stdin, ok := cmd.InOrStdin().(*os.File); opts.candidates == "" && ok && stdin == os.Stdin && isTerminalFunc() {
		return "", nil, nil, nil, fmt.Errorf("no candidates provided; pipe newline-delimited candidates to stdin or pass --candidates")
	}

	candidates, err := loadCandidates(cmd.InOrStdin(), opts.candidates)
	if err != nil {
		return "", nil, nil, nil, fmt.Errorf("failed to load candidates: %w", err)
	}
	if err := validateRunIdentityFlags(opts, candidates, mode); err != nil {
		return "", nil, nil, nil, err
	}

	selectedCandidates := candidates
	var splitResponse *testresultsv1.SplitTestsResponse
	if opts.total > 1 {
		switch mode {
		case splitModeTimings:
			selectedCandidates, splitResponse, err = splitCandidatesByTimings(cmd.Context(), candidates, opts)
			if err != nil {
				return "", nil, nil, nil, err
			}
		case splitModeName, splitModeFileSize:
			selectedCandidates, err = partitionCandidates(candidates, mode, opts.index, opts.total)
			if err != nil {
				return "", nil, nil, nil, err
			}
		}
	}

	return mode, candidates, selectedCandidates, splitResponse, nil
}

func validateRunIdentityFlags(opts runOptions, candidates []string, mode splitMode) error {
	candidateIdentity, err := resolveCandidateIdentity(opts.candidateType, candidates)
	if err != nil {
		return err
	}
	timingIdentity, err := resolveTimingIdentity(opts.timingsType, candidateIdentity)
	if err != nil {
		return err
	}
	if mode == splitModeTimings &&
		timingIdentity != testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_TESTNAME &&
		timingIdentity != timingIdentityForCandidate(candidateIdentity) {
		return fmt.Errorf("--timings-type must match --candidate-type unless --timings-type testname is used")
	}
	return nil
}

func splitCandidatesByTimings(ctx context.Context, candidates []string, opts runOptions) ([]string, *testresultsv1.SplitTestsResponse, error) {
	candidateIdentity, err := resolveCandidateIdentity(opts.candidateType, candidates)
	if err != nil {
		return nil, nil, err
	}
	timingIdentity, err := resolveTimingIdentity(opts.timingsType, candidateIdentity)
	if err != nil {
		return nil, nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, splitTestsRequestTimeout)
	defer cancel()

	token, err := resolveOIDCCredentialFunc(requestCtx)
	if err != nil {
		return nil, nil, err
	}

	resp, err := splitTestsFunc(requestCtx, token, &testresultsv1.SplitTestsRequest{
		Candidates:        candidates,
		CandidateIdentity: candidateIdentity,
		TimingIdentity:    timingIdentity,
		ShardIndex:        uint32(opts.index),
		ShardTotal:        uint32(opts.total),
		SplitKey:          testInvocationKey(opts.key),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to split tests by timings: %w", err)
	}
	return resp.GetCandidates(), resp, nil
}

func uploadTestReports(ctx context.Context, opts runOptions) error {
	_, err := uploadTestReportsResponse(ctx, opts)
	return err
}

func uploadTestReportsResponse(ctx context.Context, opts runOptions) (*testresultsv1.ReportTestResultsResponse, error) {
	workspace, err := workingDirectoryFunc()
	if err != nil {
		return nil, err
	}
	files, err := discoverReportFiles(opts.reportPath, workspace)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no test report files matched: %s", strings.Join(strings.Fields(opts.reportPath), ", "))
	}

	prepared, err := prepareReportFiles(files)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, reportTestResultsRequestTimeout)
	defer cancel()

	token, err := resolveOIDCCredentialFunc(requestCtx)
	if err != nil {
		return nil, err
	}

	resp, err := reportTestResultsFunc(requestCtx, token, &testresultsv1.ReportTestResultsRequest{
		InvocationId: testInvocationKey(opts.key),
		Files:        prepared,
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func testInvocationKey(key string) string {
	if strings.TrimSpace(key) != "" {
		return strings.TrimSpace(key)
	}
	if action := strings.TrimSpace(os.Getenv("GITHUB_ACTION")); action != "" {
		return action
	}
	return "default"
}

func validateExplicitRunIdentityFlags(opts runOptions) error {
	switch strings.ToLower(strings.TrimSpace(opts.candidateType)) {
	case "", "filename", "classname":
	default:
		return fmt.Errorf("unknown candidate type %q. Requires filename or classname", opts.candidateType)
	}

	switch strings.ToLower(strings.TrimSpace(opts.timingsType)) {
	case "", "filename", "classname", "testname":
	default:
		return fmt.Errorf("unknown timings type %q. Requires filename, classname, or testname", opts.timingsType)
	}
	return nil
}

func resolveCandidateIdentity(value string, candidates []string) (testresultsv1.TestCandidateIdentity, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		if inferFilenameCandidates(candidates) {
			return testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME, nil
		}
		return testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_CLASSNAME, nil
	case "filename":
		return testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME, nil
	case "classname":
		return testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_CLASSNAME, nil
	default:
		return testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_UNSPECIFIED, fmt.Errorf("unknown candidate type %q. Requires filename or classname", value)
	}
}

func resolveTimingIdentity(value string, candidateIdentity testresultsv1.TestCandidateIdentity) (testresultsv1.TestTimingIdentity, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		switch candidateIdentity {
		case testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME:
			return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_FILENAME, nil
		case testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_CLASSNAME:
			return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_CLASSNAME, nil
		default:
			return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_UNSPECIFIED, fmt.Errorf("candidate type is required")
		}
	case "filename":
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_FILENAME, nil
	case "classname":
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_CLASSNAME, nil
	case "testname":
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_TESTNAME, nil
	default:
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_UNSPECIFIED, fmt.Errorf("unknown timings type %q. Requires filename, classname, or testname", value)
	}
}

func timingIdentityForCandidate(candidateIdentity testresultsv1.TestCandidateIdentity) testresultsv1.TestTimingIdentity {
	switch candidateIdentity {
	case testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME:
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_FILENAME
	case testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_CLASSNAME:
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_CLASSNAME
	default:
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_UNSPECIFIED
	}
}

func inferFilenameCandidates(candidates []string) bool {
	for _, candidate := range candidates {
		if looksLikeFilenameCandidate(candidate) {
			return true
		}
	}
	return false
}

func looksLikeFilenameCandidate(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	if strings.ContainsAny(candidate, `/\`) {
		return true
	}
	ext := strings.ToLower(strings.TrimPrefix(pathExt(candidate), "."))
	switch ext {
	case "go", "js", "jsx", "ts", "tsx", "rb", "py", "php", "rs", "java", "kt", "cs", "cpp", "cc", "c", "h", "hpp", "m", "mm", "swift", "scala", "clj", "ex", "exs":
		return true
	default:
		return false
	}
}

func pathExt(value string) string {
	lastSlash := strings.LastIndexAny(value, `/\`)
	if lastSlash >= 0 {
		value = value[lastSlash+1:]
	}
	lastDot := strings.LastIndexByte(value, '.')
	if lastDot <= 0 {
		return ""
	}
	return value[lastDot:]
}

func writeSplitSummary(w io.Writer, mode splitMode, opts runOptions, totalCandidates int, selected []string, resp *testresultsv1.SplitTestsResponse) {
	if opts.total == 1 {
		fmt.Fprintf(w, "Depot selected all %d candidate(s) for a single shard.\n", len(selected))
		return
	}
	fmt.Fprintf(w, "Depot selected %d of %d candidate(s) for shard %d/%d using %s.\n", len(selected), totalCandidates, opts.index, opts.total, mode)
	if resp != nil {
		fmt.Fprintf(w, "Depot found timings for %d candidate(s).\n", resp.GetCandidatesWithTimings())
	}
}

func runShellCommand(ctx context.Context, command string, candidates []string, stdout, stderr io.Writer) (int, error) {
	shell, args := shellCommand(command)
	subCmd := exec.CommandContext(ctx, shell, args...)
	if len(candidates) == 0 {
		subCmd.Stdin = strings.NewReader("")
	} else {
		subCmd.Stdin = strings.NewReader(strings.Join(candidates, "\n") + "\n")
	}
	subCmd.Stdout = stdout
	subCmd.Stderr = stderr

	err := subCmd.Run()
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), err
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

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return shell, []string{"-c", command}
}

func commandStatus(err error, exitCode int) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("test command exited with status %d", exitCode)
}
