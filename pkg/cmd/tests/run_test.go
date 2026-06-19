package tests

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/docker/cli/cli"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
)

func TestRunDefaultsToTimingSplit(t *testing.T) {
	resetTestHooks(t)
	t.Setenv("GITHUB_ACTION", "")
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	workingDirectoryFunc = func() (string, error) { return workspace, nil }
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }

	var splitReq *testresultsv1.SplitTestsRequest
	splitTestsFunc = func(ctx context.Context, token string, req *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		requireContextDeadline(t, ctx, "split tests")
		if token != "oidc-token" {
			t.Fatalf("expected OIDC token, got %q", token)
		}
		splitReq = req
		return &testresultsv1.SplitTestsResponse{
			Candidates:            []string{"b.test.ts"},
			CandidatesRequested:   2,
			CandidatesSelected:    1,
			CandidatesWithTimings: 1,
		}, nil
	}

	var commandCandidates []string
	runShellCommandFunc = func(_ context.Context, command string, candidates []string, stdout, stderr io.Writer) (int, error) {
		if command != "test-command" {
			t.Fatalf("expected command %q, got %q", "test-command", command)
		}
		commandCandidates = append([]string(nil), candidates...)
		return 0, nil
	}

	var reportReq *testresultsv1.ReportTestResultsRequest
	reportTestResultsFunc = func(ctx context.Context, token string, req *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		requireContextDeadline(t, ctx, "report test results")
		if token != "oidc-token" {
			t.Fatalf("expected OIDC token, got %q", token)
		}
		reportReq = req
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: 1, TestsReported: 1}, nil
	}

	out, err := executeCommandWithInput(
		"a.test.ts\nb.test.ts\n",
		"run",
		"--index", "1",
		"--total", "2",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	if err != nil {
		t.Fatal(err)
	}

	if splitReq == nil {
		t.Fatal("expected SplitTests to be called")
	}
	if splitReq.GetCandidateIdentity() != testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME {
		t.Fatalf("expected inferred filename candidate identity, got %v", splitReq.GetCandidateIdentity())
	}
	if splitReq.GetTimingIdentity() != testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_FILENAME {
		t.Fatalf("expected default filename timing identity, got %v", splitReq.GetTimingIdentity())
	}
	if splitReq.GetShardIndex() != 1 || splitReq.GetShardTotal() != 2 {
		t.Fatalf("expected shard 1/2, got %d/%d", splitReq.GetShardIndex(), splitReq.GetShardTotal())
	}
	if !equalStrings(commandCandidates, []string{"b.test.ts"}) {
		t.Fatalf("expected selected command candidate, got %v", commandCandidates)
	}
	if reportReq == nil || reportReq.GetInvocationId() != "default" || len(reportReq.GetFiles()) != 1 {
		t.Fatalf("expected default report upload, got %#v", reportReq)
	}
	if !strings.Contains(out, "using timings") {
		t.Fatalf("expected timing summary, got %q", out)
	}
}

func TestRunTotalOnePassesAllCandidatesWithoutSplitting(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	workingDirectoryFunc = func() (string, error) { return workspace, nil }
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		t.Fatal("SplitTests should not be called for total=1")
		return nil, nil
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: 1, TestsReported: 1}, nil
	}

	var commandCandidates []string
	runShellCommandFunc = func(_ context.Context, _ string, candidates []string, stdout, stderr io.Writer) (int, error) {
		commandCandidates = append([]string(nil), candidates...)
		return 0, nil
	}

	_, err := executeCommandWithInput(
		"missing-a.test.ts\nmissing-b.test.ts\n",
		"run",
		"--split-by", "filesize",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(commandCandidates, []string{"missing-a.test.ts", "missing-b.test.ts"}) {
		t.Fatalf("expected all candidates, got %v", commandCandidates)
	}
}

func TestRunSubcommandTakesPrecedenceOverResultID(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	workingDirectoryFunc = func() (string, error) { return workspace, nil }
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	ciGetRunStatusFunc = func(context.Context, string, string, string) (*civ1.GetRunStatusResponse, error) {
		t.Fatal("parent tests command should not try to resolve run as an ID")
		return nil, nil
	}
	listTestResultsFunc = func(context.Context, string, string, *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		t.Fatal("parent tests command should not list test results")
		return nil, nil
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: 1}, nil
	}

	var commandRan bool
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		commandRan = true
		return 0, nil
	}

	_, err := executeCommandWithInput(
		"a.test.ts\n",
		"run",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !commandRan {
		t.Fatal("expected run subcommand to execute")
	}
}

func TestRunPassesExplicitIdentitiesAndSplitKey(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	workingDirectoryFunc = func() (string, error) { return workspace, nil }
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) { return 0, nil }
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return &testresultsv1.ReportTestResultsResponse{}, nil
	}

	var splitReq *testresultsv1.SplitTestsRequest
	splitTestsFunc = func(_ context.Context, _ string, req *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		splitReq = req
		return &testresultsv1.SplitTestsResponse{Candidates: []string{"pkg/a"}}, nil
	}

	_, err := executeCommandWithInput(
		"pkg/a\npkg/b\n",
		"run",
		"--candidate-type", "classname",
		"--timings-type", "testname",
		"--split-key", "unit",
		"--index", "0",
		"--total", "2",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if splitReq.GetCandidateIdentity() != testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_CLASSNAME {
		t.Fatalf("expected classname candidate identity, got %v", splitReq.GetCandidateIdentity())
	}
	if splitReq.GetTimingIdentity() != testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_TESTNAME {
		t.Fatalf("expected testname timing identity, got %v", splitReq.GetTimingIdentity())
	}
	if splitReq.GetSplitKey() != "unit" {
		t.Fatalf("expected split key unit, got %q", splitReq.GetSplitKey())
	}
}

func TestRunPreservesCommandExitStatusAfterReportUpload(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	workingDirectoryFunc = func() (string, error) { return workspace, nil }
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return &testresultsv1.SplitTestsResponse{Candidates: []string{"a.test.ts"}}, nil
	}
	var calls []string
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		calls = append(calls, "command")
		return 7, errors.New("exit status 7")
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		calls = append(calls, "report")
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: 1}, nil
	}

	_, err := executeCommandWithInput(
		"a.test.ts\n",
		"run",
		"--index", "0",
		"--total", "2",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	statusErr, ok := err.(cli.StatusError)
	if !ok {
		t.Fatalf("expected cli.StatusError, got %T: %v", err, err)
	}
	if statusErr.StatusCode != 7 {
		t.Fatalf("expected status code 7, got %d", statusErr.StatusCode)
	}
	if !equalStrings(calls, []string{"command", "report"}) {
		t.Fatalf("expected report upload after command failure, got calls %v", calls)
	}
}

func TestRunFailsWhenReportUploadFailsAfterSuccessfulCommand(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	workingDirectoryFunc = func() (string, error) { return workspace, nil }
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return &testresultsv1.SplitTestsResponse{Candidates: []string{"a.test.ts"}}, nil
	}
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) { return 0, nil }
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return nil, errors.New("upload failed")
	}

	_, err := executeCommandWithInput(
		"a.test.ts\n",
		"run",
		"--index", "0",
		"--total", "2",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	if err == nil || !strings.Contains(err.Error(), "failed to upload test reports") {
		t.Fatalf("expected report upload error, got %v", err)
	}
}

func TestRunValidationFailsBeforeCommandExecution(t *testing.T) {
	resetTestHooks(t)
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("command should not run")
		return 0, nil
	}

	_, err := executeCommandWithInput("a.test.ts\n", "run", "--index", "0", "--total", "2", "--report-path", "junit.xml")
	if err == nil || !strings.Contains(err.Error(), "--command is required") {
		t.Fatalf("expected command validation error, got %v", err)
	}
}

func TestRunValidatesIdentityFlagsBeforeSingleShardPassThrough(t *testing.T) {
	resetTestHooks(t)
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("command should not run")
		return 0, nil
	}

	_, err := executeCommandWithInput(
		"a.test.ts\n",
		"run",
		"--candidate-type", "suite",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "junit.xml",
	)
	if err == nil || !strings.Contains(err.Error(), "unknown candidate type") {
		t.Fatalf("expected candidate type validation error, got %v", err)
	}
}

func TestRunValidatesExplicitIdentityFlagsBeforeReadingStdin(t *testing.T) {
	resetTestHooks(t)
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("command should not run")
		return 0, nil
	}

	_, err := executeCommand(
		"run",
		"--candidate-type", "suite",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "junit.xml",
	)
	if err == nil || !strings.Contains(err.Error(), "unknown candidate type") {
		t.Fatalf("expected candidate type validation error, got %v", err)
	}
}

func TestRunRequiresCandidatesWhenStdinIsTerminal(t *testing.T) {
	resetTestHooks(t)
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("command should not run")
		return 0, nil
	}

	_, err := executeCommand(
		"run",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "junit.xml",
	)
	if err == nil || !strings.Contains(err.Error(), "pipe newline-delimited candidates") {
		t.Fatalf("expected missing candidates error, got %v", err)
	}
}

func TestRunRejectsUnsupportedTimingIdentityPair(t *testing.T) {
	resetTestHooks(t)
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("command should not run")
		return 0, nil
	}
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		t.Fatal("SplitTests should not be called for invalid identity pairs")
		return nil, nil
	}

	_, err := executeCommandWithInput(
		"a.test.ts\n",
		"run",
		"--candidate-type", "filename",
		"--timings-type", "classname",
		"--index", "0",
		"--total", "2",
		"--command", "test-command",
		"--report-path", "junit.xml",
	)
	if err == nil || !strings.Contains(err.Error(), "must match --candidate-type") {
		t.Fatalf("expected identity pair validation error, got %v", err)
	}
}

func TestRunShellCommandWritesCandidatesToStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell utilities")
	}

	var stdout, stderr bytes.Buffer
	exitCode, err := runShellCommand(context.Background(), "cat", []string{"a.test.ts", "b.test.ts"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if stdout.String() != "a.test.ts\nb.test.ts\n" {
		t.Fatalf("expected candidates on stdin, got stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestRunShellCommandWritesEmptyStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell syntax")
	}

	var stdout, stderr bytes.Buffer
	exitCode, err := runShellCommand(context.Background(), "if read line; then echo unexpected; exit 1; fi", nil, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d stdout %q stderr %q", exitCode, stdout.String(), stderr.String())
	}
}

func TestRunShellCommandReturnsExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell syntax")
	}

	var stdout, stderr bytes.Buffer
	exitCode, err := runShellCommand(context.Background(), "exit 9", nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected command error")
	}
	if exitCode != 9 {
		t.Fatalf("expected exit code 9, got %d", exitCode)
	}
}

func executeCommandWithInput(input string, args ...string) (string, error) {
	cmd := NewCmdTests()
	var out bytes.Buffer
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func requireContextDeadline(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatalf("expected %s context to have deadline", name)
	}
}
