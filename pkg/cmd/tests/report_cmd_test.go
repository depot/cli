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
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite><testcase name=\"a\"/></testsuite>")
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		t.Fatal("report should not split tests")
		return nil, nil
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

func TestReportUsesDefaultTestKey(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	t.Setenv("GITHUB_ACTION", "unit-tests")
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")

	var invocationID string
	reportTestResultsFunc = func(_ context.Context, _ string, req *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		invocationID = req.GetInvocationId()
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: 1}, nil
	}

	_, _, err := executeCommandWithInputOutput("", "report", "--report-path", "reports/junit.xml")
	if err != nil {
		t.Fatal(err)
	}
	if invocationID != "unit-tests" {
		t.Fatalf("expected report to reuse default test key, got %q", invocationID)
	}
}

func TestReportUsesGitHubWorkspace(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	other := t.TempDir()
	t.Chdir(other)
	t.Setenv("GITHUB_WORKSPACE", workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")

	var filenames []string
	reportTestResultsFunc = func(_ context.Context, _ string, req *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		for _, file := range req.GetFiles() {
			filenames = append(filenames, file.GetFilename())
		}
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: uint32(len(req.GetFiles()))}, nil
	}

	_, _, err := executeCommandWithInputOutput("", "report", "--report-path", "reports/junit.xml")
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(filenames, []string{"reports/junit.xml"}) {
		t.Fatalf("expected report path to resolve against GITHUB_WORKSPACE, got %v", filenames)
	}
}

func TestReportSupportsJSONOutput(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
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
	if out.FilesUploaded != 1 || out.FilesProcessed != 1 || out.TestsReported != 2 || !out.DuplicateInvocation || out.FilesSkipped != 3 {
		t.Fatalf("expected report counts in json, got %#v", out)
	}
}

func TestReportTextOutputMentionsDuplicateInvocation(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return &testresultsv1.ReportTestResultsResponse{DuplicateInvocation: true}, nil
	}

	stdout, _, err := executeCommandWithInputOutput("", "report", "--report-path", "reports/junit.xml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "already reported") {
		t.Fatalf("expected duplicate invocation summary, got %q", stdout)
	}
}

func TestReportAcceptsRepeatedReportPathsAndPositionalPaths(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/a.xml", "<testsuite/>")
	writeTempFileAt(t, workspace, "reports/b.xml", "<testsuite/>")

	var filenames []string
	reportTestResultsFunc = func(_ context.Context, _ string, req *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		for _, file := range req.GetFiles() {
			filenames = append(filenames, file.GetFilename())
		}
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: uint32(len(req.GetFiles()))}, nil
	}

	_, _, err := executeCommandWithInputOutput(
		"",
		"report",
		"--report-path", "reports/a.xml",
		"reports/b.xml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(filenames, []string{"reports/a.xml", "reports/b.xml"}) {
		t.Fatalf("expected uploaded filenames to preserve discovered sorted paths, got %v", filenames)
	}
}

func TestReportAcceptsMultilineReportPath(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/a.xml", "<testsuite/>")
	writeTempFileAt(t, workspace, "reports/b.xml", "<testsuite/>")

	var filenames []string
	reportTestResultsFunc = func(_ context.Context, _ string, req *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		for _, file := range req.GetFiles() {
			filenames = append(filenames, file.GetFilename())
		}
		return &testresultsv1.ReportTestResultsResponse{FilesProcessed: uint32(len(req.GetFiles()))}, nil
	}

	_, _, err := executeCommandWithInputOutput(
		"",
		"report",
		"--report-path", "reports/a.xml\nreports/b.xml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(filenames, []string{"reports/a.xml", "reports/b.xml"}) {
		t.Fatalf("expected multiline report path to upload both files, got %v", filenames)
	}
}

func TestReportRequiresReportPath(t *testing.T) {
	resetTestHooks(t)
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		t.Fatal("report should validate before uploading")
		return nil, nil
	}

	_, _, err := executeCommandOutput("report")
	if err == nil || !strings.Contains(err.Error(), "at least one report path is required") {
		t.Fatalf("expected report path validation error, got %v", err)
	}
}

func TestReportRejectsUnknownOutput(t *testing.T) {
	resetTestHooks(t)
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		t.Fatal("report should validate output before uploading")
		return nil, nil
	}

	_, _, err := executeCommandOutput("report", "--report-path", "reports/junit.xml", "--output", "auto")
	if err == nil || !strings.Contains(err.Error(), "Requires text or json") {
		t.Fatalf("expected output validation error, got %v", err)
	}
}

func TestReportSubcommandTakesPrecedenceOverResultID(t *testing.T) {
	resetTestHooks(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeTempFileAt(t, workspace, "reports/junit.xml", "<testsuite/>")
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
