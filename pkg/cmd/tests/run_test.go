package tests

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/docker/cli/cli"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
)

func TestRunDefaultsToTimingSplitAndReportsWithKey(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
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
		writeRunReport(t, workspace)
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

	stdout, stderr, err := executeCommandWithInputOutput(
		"a.test.ts\nb.test.ts\n",
		"run",
		"--candidate-type", "filename",
		"--timings-type", "testname",
		"--key", "unit",
		"--index", "1",
		"--total", "2",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	if err != nil {
		t.Fatal(err)
	}

	if stdout != "" {
		t.Fatalf("expected run command to preserve test command stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "using timings") || !strings.Contains(stderr, "Depot found timings for 1 candidate") {
		t.Fatalf("expected timing summary on stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "Depot uploaded 1 test report file") || !strings.Contains(stderr, "reported 1 test") {
		t.Fatalf("expected report summary on stderr, got %q", stderr)
	}
	if splitReq == nil {
		t.Fatal("expected SplitTests to be called")
	}
	if splitReq.GetCandidateIdentity() != testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME {
		t.Fatalf("expected filename candidate identity, got %v", splitReq.GetCandidateIdentity())
	}
	if splitReq.GetTimingIdentity() != testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_TESTNAME {
		t.Fatalf("expected testname timing identity, got %v", splitReq.GetTimingIdentity())
	}
	if splitReq.GetSplitKey() != "unit" {
		t.Fatalf("expected split key unit, got %q", splitReq.GetSplitKey())
	}
	if !equalStrings(commandCandidates, []string{"b.test.ts"}) {
		t.Fatalf("expected selected command candidate, got %v", commandCandidates)
	}
	if reportReq == nil || reportReq.GetInvocationId() != "unit" || len(reportReq.GetFiles()) != 1 {
		t.Fatalf("expected report upload with unit key, got %#v", reportReq)
	}
}

func TestRunWithoutShardFlagsRunsAllCandidatesWithoutSplitting(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		t.Fatal("SplitTests should not be called without shard flags")
		return nil, nil
	}

	var commandCandidates []string
	runShellCommandFunc = func(_ context.Context, _ string, candidates []string, _ io.Writer, _ io.Writer) (int, error) {
		commandCandidates = append([]string(nil), candidates...)
		writeRunReport(t, workspace)
		return 0, nil
	}
	var reported bool
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		reported = true
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: 1}, nil
	}

	_, stderr, err := executeCommandWithInputOutput(
		"a.test.ts\nb.test.ts\n",
		"run",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(commandCandidates, []string{"a.test.ts", "b.test.ts"}) {
		t.Fatalf("expected all candidates on command stdin, got %v", commandCandidates)
	}
	if !reported {
		t.Fatal("expected report upload after command")
	}
	if !strings.Contains(stderr, "Depot selected all 2 candidate") {
		t.Fatalf("expected single shard summary, got %q", stderr)
	}
}

func TestRunPreservesRealCommandStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell syntax")
	}

	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	runShellCommandFunc = runShellCommand
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: 1, TestsReported: 1}, nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
		"a.test.ts\n",
		"run",
		"--index", "0",
		"--total", "1",
		"--command", "mkdir -p reports; printf '<testsuite><testcase name=\"updated\"/></testsuite>' > reports/junit.xml; printf 'child stdout\\n'",
		"--report-path", "reports/junit.xml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "child stdout\n" {
		t.Fatalf("expected child stdout to pass through unchanged, got %q", stdout)
	}
	if strings.Contains(stderr, "child stdout") {
		t.Fatalf("expected child stdout to stay out of stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "Depot selected all 1 candidate") || !strings.Contains(stderr, "Depot uploaded 1 test report file") {
		t.Fatalf("expected run summaries on stderr, got %q", stderr)
	}
}

func TestRunSkipsCommandAndReportForEmptySelectedShard(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return &testresultsv1.SplitTestsResponse{
			Candidates:            nil,
			CandidatesRequested:   2,
			CandidatesSelected:    0,
			CandidatesWithTimings: 1,
		}, nil
	}

	runShellCommandFunc = func(_ context.Context, _ string, candidates []string, _ io.Writer, _ io.Writer) (int, error) {
		t.Fatalf("command should not run for empty shard; got candidates %v", candidates)
		return 1, nil
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		t.Fatal("report upload should not run for empty shard")
		return nil, nil
	}

	_, stderr, err := executeCommandWithInputOutput(
		"com.example.A\ncom.example.B\n",
		"run",
		"--candidate-type", "classname",
		"--index", "1",
		"--total", "2",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "selected 0 of 2 candidate") {
		t.Fatalf("expected empty shard summary, got %q", stderr)
	}
	if !strings.Contains(stderr, "Depot skipped test command because this shard has no candidates.") {
		t.Fatalf("expected empty shard skip summary, got %q", stderr)
	}
}

func TestRunFallsBackToFileSizeWhenNoTimingsAreAvailable(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	small := writeSizedFile(t, workspace, "small.test.ts", 1)
	large := writeSizedFile(t, workspace, "large.test.ts", 100)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return &testresultsv1.SplitTestsResponse{
			Candidates:            []string{large},
			CandidatesRequested:   2,
			CandidatesSelected:    1,
			CandidatesWithTimings: 0,
		}, nil
	}

	var commandCandidates []string
	runShellCommandFunc = func(_ context.Context, _ string, candidates []string, _ io.Writer, _ io.Writer) (int, error) {
		commandCandidates = append([]string(nil), candidates...)
		writeRunReport(t, workspace)
		return 0, nil
	}

	_, stderr, err := executeCommandWithInputOutput(
		small+"\n"+large+"\n",
		"run",
		"--candidate-type", "filename",
		"--index", "1",
		"--total", "2",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(commandCandidates, []string{small}) {
		t.Fatalf("expected filesize fallback shard on command stdin, got %v", commandCandidates)
	}
	if !strings.Contains(stderr, "using filesize") {
		t.Fatalf("expected filesize fallback summary, got %q", stderr)
	}
}

func TestRunReadsCandidatesFile(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	candidatesPath := writeTempFileAt(t, workspace, "candidates.txt", "a.test.ts\nb.test.ts\n")
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")

	var commandCandidates []string
	runShellCommandFunc = func(_ context.Context, _ string, candidates []string, _ io.Writer, _ io.Writer) (int, error) {
		commandCandidates = append([]string(nil), candidates...)
		writeRunReport(t, workspace)
		return 0, nil
	}

	_, _, err := executeCommandWithInputOutput(
		"ignored.test.ts\n",
		"run",
		"--candidates-file", candidatesPath,
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(commandCandidates, []string{"a.test.ts", "b.test.ts"}) {
		t.Fatalf("expected candidates file to drive command stdin, got %v", commandCandidates)
	}
}

func TestRunSubcommandTakesPrecedenceOverResultID(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	ciGetRunStatusFunc = func(context.Context, string, string, string) (*civ1.GetRunStatusResponse, error) {
		t.Fatal("parent tests command should not try to resolve run as an ID")
		return nil, nil
	}
	listTestResultsFunc = func(context.Context, string, string, *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		t.Fatal("parent tests command should not list test results")
		return nil, nil
	}

	var commandRan bool
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		commandRan = true
		writeRunReport(t, workspace)
		return 0, nil
	}

	_, _, err := executeCommandWithInputOutput(
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

func TestRunPreservesCommandExitStatusAfterReportUpload(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return &testresultsv1.SplitTestsResponse{Candidates: []string{"a.test.ts"}, CandidatesWithTimings: 1}, nil
	}
	var calls []string
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		calls = append(calls, "command")
		writeRunReport(t, workspace)
		return 7, errors.New("exit status 7")
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		calls = append(calls, "report")
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: 1}, nil
	}

	_, _, err := executeCommandWithInputOutput(
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

func TestRunPreservesCommandExitStatusWhenReportUploadAlsoFails(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return &testresultsv1.SplitTestsResponse{Candidates: []string{"a.test.ts"}, CandidatesWithTimings: 1}, nil
	}
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		writeRunReport(t, workspace)
		return 7, errors.New("exit status 7")
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return nil, errors.New("upload failed")
	}

	_, _, err := executeCommandWithInputOutput(
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
	if !strings.Contains(statusErr.Status, "exit status 7") || !strings.Contains(statusErr.Status, "failed to upload test reports: upload failed") {
		t.Fatalf("expected command and report failures in status, got %q", statusErr.Status)
	}
}

func TestRunSkipsReportUploadAfterCancellation(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		return 143, context.Canceled
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		t.Fatal("report upload should not run after cancellation")
		return nil, nil
	}

	_, _, err := executeCommandWithInputOutput(
		"a.test.ts\n",
		"run",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	statusErr, ok := err.(cli.StatusError)
	if !ok {
		t.Fatalf("expected cli.StatusError, got %T: %v", err, err)
	}
	if statusErr.StatusCode != 1 {
		t.Fatalf("expected cancellation status code 1, got %d", statusErr.StatusCode)
	}
	if !strings.Contains(statusErr.Status, "context canceled") || !strings.Contains(statusErr.Status, "status 143") {
		t.Fatalf("expected cancellation status, got %q", statusErr.Status)
	}
}

func TestRunSkipsReportUploadWhenCanceledAfterSuccessfulCommand(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")

	ctx, cancel := context.WithCancel(context.Background())
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		writeRunReport(t, workspace)
		cancel()
		return 0, nil
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		t.Fatal("report upload should not run after cancellation")
		return nil, nil
	}

	cmd := newCmdTestsRun()
	var stdout, stderr bytes.Buffer
	cmd.SetContext(ctx)
	cmd.SetIn(strings.NewReader("a.test.ts\n"))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	})

	err := cmd.Execute()
	statusErr, ok := err.(cli.StatusError)
	if !ok {
		t.Fatalf("expected cli.StatusError, got %T: %v", err, err)
	}
	if statusErr.StatusCode != 1 {
		t.Fatalf("expected cancellation status code 1, got %d", statusErr.StatusCode)
	}
	if !strings.Contains(statusErr.Status, "context canceled") {
		t.Fatalf("expected cancellation status, got %q", statusErr.Status)
	}
}

func TestRunSplitCancellationExitsOne(t *testing.T) {
	resetTestHooks(t)

	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return nil, context.Canceled
	}
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("command should not run after split cancellation")
		return 0, nil
	}

	cmd := newCmdTestsRun()
	var stdout, stderr bytes.Buffer
	cmd.SetIn(strings.NewReader("a.test.ts\n"))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{
		"--index", "0",
		"--total", "2",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	})

	err := cmd.Execute()
	statusErr, ok := err.(cli.StatusError)
	if !ok {
		t.Fatalf("expected cli.StatusError, got %T: %v", err, err)
	}
	if statusErr.StatusCode != 1 {
		t.Fatalf("expected cancellation status code 1, got %d", statusErr.StatusCode)
	}
	if !strings.Contains(statusErr.Status, "context canceled") {
		t.Fatalf("expected cancellation status, got %q", statusErr.Status)
	}
}

func TestRunReportUploadCancellationAfterSuccessfulCommandExitsOne(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		writeRunReport(t, workspace)
		return 0, nil
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return nil, context.Canceled
	}

	_, _, err := executeCommandWithInputOutput(
		"a.test.ts\n",
		"run",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	statusErr, ok := err.(cli.StatusError)
	if !ok {
		t.Fatalf("expected cli.StatusError, got %T: %v", err, err)
	}
	if statusErr.StatusCode != 1 {
		t.Fatalf("expected cancellation status code 1, got %d", statusErr.StatusCode)
	}
	if !strings.Contains(statusErr.Status, "failed to upload test reports") {
		t.Fatalf("expected upload error status, got %q", statusErr.Status)
	}
}

func TestRunReportUploadCancellationPreservesFailedCommandExitStatus(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		writeRunReport(t, workspace)
		return 7, errors.New("exit status 7")
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return nil, context.Canceled
	}

	_, _, err := executeCommandWithInputOutput(
		"a.test.ts\n",
		"run",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	statusErr, ok := err.(cli.StatusError)
	if !ok {
		t.Fatalf("expected cli.StatusError, got %T: %v", err, err)
	}
	if statusErr.StatusCode != 7 {
		t.Fatalf("expected command status code 7, got %d", statusErr.StatusCode)
	}
	if !strings.Contains(statusErr.Status, "exit status 7") || !strings.Contains(statusErr.Status, "context canceled") {
		t.Fatalf("expected command and upload cancellation failures in status, got %q", statusErr.Status)
	}
}

func TestRunFailsWhenReportUploadFailsAfterSuccessfulCommand(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		writeRunReport(t, workspace)
		return 0, nil
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return nil, errors.New("upload failed")
	}

	_, _, err := executeCommandWithInputOutput(
		"a.test.ts\n",
		"run",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	if err == nil || !strings.Contains(err.Error(), "failed to upload test reports") {
		t.Fatalf("expected report upload error, got %v", err)
	}
}

func TestRunRejectsWhenNoReportFileWasUpdatedByCommand(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	reportPath := writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite><testcase name=\"old\"/></testsuite>")
	staleTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(reportPath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		return 0, nil
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		t.Fatal("unchanged report should not upload")
		return nil, nil
	}

	_, _, err := executeCommandWithInputOutput(
		"a.test.ts\n",
		"run",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "reports/junit.xml",
	)
	if err == nil || !strings.Contains(err.Error(), "no JUnit XML report files were updated by command") {
		t.Fatalf("expected no updated reports error, got %v", err)
	}
}

func TestRunRejectsStaleReportCreatedAfterEmptyBaseline(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	stalePath := writeTempFileAt(t, workspace, "stale/junit.xml", "<testsuite><testcase name=\"old\"/></testsuite>")
	staleTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stalePath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(stalePath, filepath.Join(workspace, "reports", "junit.xml")); err != nil {
			t.Fatal(err)
		}
		return 0, nil
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		t.Fatal("stale report should not upload")
		return nil, nil
	}

	_, _, err := executeCommandWithInputOutput(
		"a.test.ts\n",
		"run",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "reports/*.xml",
	)
	if err == nil || !strings.Contains(err.Error(), "no JUnit XML report files were updated by command") {
		t.Fatalf("expected no updated reports error, got %v", err)
	}
}

func TestRunFiltersStaleReportFilesAndUploadsUpdatedReports(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	stalePath := writeTempFileAt(t, workspace, "reports/stale.xml", "<testsuite><testcase name=\"old\"/></testsuite>")
	staleTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stalePath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		writeTempFileAt(t, workspace, "reports/fresh.xml", "<testsuite><testcase name=\"new\"/></testsuite>")
		return 0, nil
	}
	var filenames []string
	reportTestResultsFunc = func(_ context.Context, _ string, req *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		for _, file := range req.GetFiles() {
			filenames = append(filenames, file.GetFilename())
		}
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: uint32(len(req.GetFiles()))}, nil
	}

	_, _, err := executeCommandWithInputOutput(
		"a.test.ts\n",
		"run",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "reports/*.xml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(filenames, []string{"reports/fresh.xml"}) {
		t.Fatalf("expected only fresh report upload, got %v", filenames)
	}
}

func TestRunReportsDuplicateInvocationOnStderr(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		writeRunReport(t, workspace)
		return 0, nil
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return &testresultsv1.ReportTestResultsResponse{
			FilesProcessed:      1,
			DuplicateInvocation: true,
		}, nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
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
	if stdout != "" {
		t.Fatalf("expected no run-managed stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "already reported") {
		t.Fatalf("expected duplicate invocation summary on stderr, got %q", stderr)
	}
}

func TestRunValidationFailsBeforeCommandExecution(t *testing.T) {
	resetTestHooks(t)
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("command should not run")
		return 0, nil
	}

	_, _, err := executeCommandWithInputOutput("a.test.ts\n", "run", "--index", "0", "--total", "2", "--report-path", "junit.xml")
	if err == nil || !strings.Contains(err.Error(), "--command is required") {
		t.Fatalf("expected command validation error, got %v", err)
	}
}

func TestRunRequiresReportPathBeforeCommandExecution(t *testing.T) {
	resetTestHooks(t)
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("command should not run")
		return 0, nil
	}

	_, _, err := executeCommandWithInputOutput(
		"a.test.ts\n",
		"run",
		"--index", "0",
		"--total", "2",
		"--command", "test-command",
	)
	if err == nil || !strings.Contains(err.Error(), "at least one report path is required") {
		t.Fatalf("expected report path validation error, got %v", err)
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

	_, _, err := executeCommandWithInputOutput(
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

func TestRunRequiresCandidatesWhenStdinIsTerminal(t *testing.T) {
	resetTestHooks(t)
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("command should not run")
		return 0, nil
	}

	_, _, err := executeCommandOutput(
		"run",
		"--index", "0",
		"--total", "1",
		"--command", "test-command",
		"--report-path", "junit.xml",
	)
	if err == nil || !strings.Contains(err.Error(), "pipe newline-delimited candidates") || !strings.Contains(err.Error(), "--candidates-file") {
		t.Fatalf("expected missing candidates error, got %v", err)
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

func TestRunCancellableShellCommandDoesNotStartWhenAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runCancellableShellCommand(ctx, exec.Command("depot-definitely-not-a-real-command"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation before process start, got %v", err)
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

func TestRunShellCommandUsesStableUnixShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix shell path")
	}

	t.Setenv("SHELL", "/bin/false")

	shell, args := shellCommand("printf ok")
	if shell != "/bin/sh" {
		t.Fatalf("expected stable /bin/sh shell, got %q with args %v", shell, args)
	}

	var stdout, stderr bytes.Buffer
	exitCode, err := runShellCommand(context.Background(), "printf ok", nil, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if stdout.String() != "ok" {
		t.Fatalf("expected command to run through /bin/sh, got stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func writeRunReport(t *testing.T, workspace string) {
	t.Helper()
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite><testcase name=\"updated\"/></testsuite>")
}
