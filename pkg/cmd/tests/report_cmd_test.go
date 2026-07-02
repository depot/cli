package tests

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
)

func TestReportUploadsReportsWithKey(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite><testcase name=\"a\"/></testsuite>")
	workingDirectoryFunc = func() (string, error) { return workspace, nil }
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		t.Fatal("report should not split tests")
		return nil, nil
	}
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("report should not run a command")
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
		"",
		"report",
		"--report-path", "reports/junit.xml",
		"--key", "unit",
	)
	if err != nil {
		t.Fatal(err)
	}

	if stderr != "" {
		t.Fatalf("expected no stderr output, got %q", stderr)
	}
	if !strings.Contains(stdout, "uploaded 1 test report file") || !strings.Contains(stdout, "reported 1 test") {
		t.Fatalf("expected report summary, got %q", stdout)
	}
	if reportReq.GetInvocationId() != "unit" {
		t.Fatalf("expected report invocation key unit, got %q", reportReq.GetInvocationId())
	}
	if len(reportReq.GetFiles()) != 1 || reportReq.GetFiles()[0].GetFilename() != "reports/junit.xml" {
		t.Fatalf("expected prepared junit report, got %#v", reportReq.GetFiles())
	}
	if contents := gunzipTestReport(t, reportReq.GetFiles()[0].GetGzippedXml()); !strings.Contains(contents, "<testcase name=\"a\"/>") {
		t.Fatalf("expected gzipped report contents, got %q", contents)
	}
}

func TestReportSupportsJSONOutput(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	workingDirectoryFunc = func() (string, error) { return workspace, nil }
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return &testresultsv1.ReportTestResultsResponse{
			FilesProcessed:      1,
			TestsReported:       2,
			DuplicateInvocation: true,
			FilesSkipped:        3,
		}, nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
		"",
		"report",
		"--report-path", "reports/junit.xml",
		"--output", "json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr output, got %q", stderr)
	}

	var out reportOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("expected valid json, got %q: %v", stdout, err)
	}
	if out.FilesProcessed != 1 || out.TestsReported != 2 || !out.DuplicateInvocation || out.FilesSkipped != 3 {
		t.Fatalf("expected report counts in json, got %#v", out)
	}
}

func TestReportRequiresReportPath(t *testing.T) {
	resetTestHooks(t)
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		t.Fatal("report should validate before uploading")
		return nil, nil
	}

	_, _, err := executeCommandOutput("report")
	if err == nil || !strings.Contains(err.Error(), "--report-path is required") {
		t.Fatalf("expected report path validation error, got %v", err)
	}
}

func TestReportSubcommandTakesPrecedenceOverResultID(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	workingDirectoryFunc = func() (string, error) { return workspace, nil }
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	ciGetRunStatusFunc = func(context.Context, string, string, string) (*civ1.GetRunStatusResponse, error) {
		t.Fatal("parent tests command should not try to resolve report as an ID")
		return nil, nil
	}
	listTestResultsFunc = func(context.Context, string, string, *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		t.Fatal("parent tests command should not list test results")
		return nil, nil
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: 1}, nil
	}

	stdout, _, err := executeCommandWithInputOutput(
		"",
		"report",
		"--report-path", "reports/junit.xml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "uploaded 1 test report file") {
		t.Fatalf("expected report subcommand output, got %q", stdout)
	}
}

func gunzipTestReport(t *testing.T, contents []byte) string {
	t.Helper()

	reader, err := gzip.NewReader(bytes.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
