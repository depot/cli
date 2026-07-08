package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
)

func TestRunListsDirectCIAttemptWhenRunLookupFails(t *testing.T) {
	resetTestHooks(t)

	var got *testresultsv1.ListTestResultsRequest
	ciGetRunStatusFunc = func(context.Context, string, string, string) (*civ1.GetRunStatusResponse, error) {
		return nil, errors.New("not a run")
	}
	listTestResultsFunc = func(_ context.Context, token, orgID string, req *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		if token != "token-1" {
			t.Fatalf("expected token %q, got %q", "token-1", token)
		}
		if orgID != "org-1" {
			t.Fatalf("expected org %q, got %q", "org-1", orgID)
		}
		got = req
		return &testresultsv1.ListTestResultsResponse{OwnerId: req.GetOwnerId()}, nil
	}

	_, err := executeCommand(
		"attempt-1",
		"--ci",
		"--status",
		"failed",
		"--suite",
		"pkg",
		"--test",
		"TestFails",
		"--class",
		"PackageTest",
		"--file",
		"pkg/package_test.go",
		"--page-size",
		"50",
		"--page-token",
		"next-token",
	)
	if err != nil {
		t.Fatal(err)
	}

	if got.GetOwnerType() != testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_CI {
		t.Fatalf("expected CI owner type, got %v", got.GetOwnerType())
	}
	if got.GetOwnerId() != "attempt-1" {
		t.Fatalf("expected owner ID %q, got %q", "attempt-1", got.GetOwnerId())
	}
	if len(got.GetStatus()) != 1 || got.GetStatus()[0] != testresultsv1.TestResultStatus_TEST_RESULT_STATUS_FAILED {
		t.Fatalf("expected failed status filter, got %v", got.GetStatus())
	}
	if got.GetSuiteName() != "pkg" || got.GetTestName() != "TestFails" {
		t.Fatalf("expected suite/test filters, got %q/%q", got.GetSuiteName(), got.GetTestName())
	}
	if got.GetClassName() != "PackageTest" || got.GetFileName() != "pkg/package_test.go" {
		t.Fatalf("expected class/file filters, got %q/%q", got.GetClassName(), got.GetFileName())
	}
	if got.GetPageSize() != 50 || got.GetPageToken() != "next-token" {
		t.Fatalf("expected pagination options, got size=%d token=%q", got.GetPageSize(), got.GetPageToken())
	}
}

func TestRunResolvesCIRunToLatestAttempt(t *testing.T) {
	resetTestHooks(t)

	ciGetRunStatusFunc = func(_ context.Context, _ string, _ string, id string) (*civ1.GetRunStatusResponse, error) {
		if id != "run-1" {
			t.Fatalf("expected run lookup for %q, got %q", "run-1", id)
		}
		return &civ1.GetRunStatusResponse{
			RunId: "run-1",
			Workflows: []*civ1.WorkflowStatus{
				{
					WorkflowPath: ".depot/workflows/ci.yml",
					Jobs: []*civ1.JobStatus{
						{
							JobId:  "job-1",
							JobKey: "test",
							Attempts: []*civ1.AttemptStatus{
								{AttemptId: "attempt-1", Attempt: 1},
								{AttemptId: "attempt-2", Attempt: 2},
							},
						},
					},
				},
			},
		}, nil
	}

	var got *testresultsv1.ListTestResultsRequest
	listTestResultsFunc = func(_ context.Context, _, _ string, req *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		got = req
		return &testresultsv1.ListTestResultsResponse{OwnerId: req.GetOwnerId()}, nil
	}

	_, err := executeCommand("run-1", "--ci", "--job", "test")
	if err != nil {
		t.Fatal(err)
	}

	if got.GetOwnerId() != "attempt-2" {
		t.Fatalf("expected latest attempt %q, got %q", "attempt-2", got.GetOwnerId())
	}
}

func TestRunResolvesCIJobWhenJobFlagIsSetWithoutBackend(t *testing.T) {
	resetTestHooks(t)

	ciGetRunStatusFunc = func(_ context.Context, _ string, _ string, id string) (*civ1.GetRunStatusResponse, error) {
		if id != "run-1" {
			t.Fatalf("expected run lookup for %q, got %q", "run-1", id)
		}
		return &civ1.GetRunStatusResponse{
			RunId: "run-1",
			Workflows: []*civ1.WorkflowStatus{
				{
					WorkflowPath: ".depot/workflows/ci.yml",
					Jobs: []*civ1.JobStatus{
						{
							JobId:  "job-1",
							JobKey: "test",
							Attempts: []*civ1.AttemptStatus{
								{AttemptId: "attempt-1", Attempt: 1},
								{AttemptId: "attempt-2", Attempt: 2},
							},
						},
					},
				},
			},
		}, nil
	}

	var got *testresultsv1.ListTestResultsRequest
	listTestResultsFunc = func(_ context.Context, _, _ string, req *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		got = req
		return &testresultsv1.ListTestResultsResponse{OwnerId: req.GetOwnerId()}, nil
	}

	_, err := executeCommand("run-1", "--job", "test")
	if err != nil {
		t.Fatal(err)
	}

	if got.GetOwnerType() != testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_CI {
		t.Fatalf("expected CI owner type, got %v", got.GetOwnerType())
	}
	if got.GetOwnerId() != "attempt-2" {
		t.Fatalf("expected latest attempt %q, got %q", "attempt-2", got.GetOwnerId())
	}
}

func TestRunResolvesCIJobIDWhenBackendOmitted(t *testing.T) {
	resetTestHooks(t)

	ciGetRunStatusFunc = func(_ context.Context, _ string, _ string, id string) (*civ1.GetRunStatusResponse, error) {
		if id != "job-1" {
			t.Fatalf("expected run lookup for %q, got %q", "job-1", id)
		}
		return &civ1.GetRunStatusResponse{
			RunId: "run-1",
			Workflows: []*civ1.WorkflowStatus{
				{
					WorkflowPath: ".depot/workflows/ci.yml",
					Jobs: []*civ1.JobStatus{
						{
							JobId:  "job-1",
							JobKey: "test",
							Attempts: []*civ1.AttemptStatus{
								{AttemptId: "attempt-1", Attempt: 1},
								{AttemptId: "attempt-2", Attempt: 2},
							},
						},
					},
				},
			},
		}, nil
	}

	var got *testresultsv1.ListTestResultsRequest
	listTestResultsFunc = func(_ context.Context, _, _ string, req *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		got = req
		return &testresultsv1.ListTestResultsResponse{OwnerId: req.GetOwnerId()}, nil
	}

	_, err := executeCommand("job-1")
	if err != nil {
		t.Fatal(err)
	}

	if got.GetOwnerType() != testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_CI {
		t.Fatalf("expected CI owner type, got %v", got.GetOwnerType())
	}
	if got.GetOwnerId() != "attempt-2" {
		t.Fatalf("expected latest attempt %q, got %q", "attempt-2", got.GetOwnerId())
	}
}

func TestRunListsGitHubActionsJob(t *testing.T) {
	resetTestHooks(t)

	var got *testresultsv1.ListTestResultsRequest
	listTestResultsFunc = func(_ context.Context, _, _ string, req *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		got = req
		return &testresultsv1.ListTestResultsResponse{OwnerId: req.GetOwnerId()}, nil
	}

	_, err := executeCommand("123456789", "--gha", "--status", "failed", "--status", "errored")
	if err != nil {
		t.Fatal(err)
	}

	if got.GetOwnerType() != testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_GITHUB_ACTIONS {
		t.Fatalf("expected GitHub Actions owner type, got %v", got.GetOwnerType())
	}
	if got.GetOwnerId() != "123456789" {
		t.Fatalf("expected owner ID %q, got %q", "123456789", got.GetOwnerId())
	}
	wantStatuses := []testresultsv1.TestResultStatus{
		testresultsv1.TestResultStatus_TEST_RESULT_STATUS_FAILED,
		testresultsv1.TestResultStatus_TEST_RESULT_STATUS_ERRORED,
	}
	if !equalStatuses(got.GetStatus(), wantStatuses) {
		t.Fatalf("expected statuses %v, got %v", wantStatuses, got.GetStatus())
	}
}

func TestRunListsUnspecifiedBackendWhenBackendOmitted(t *testing.T) {
	resetTestHooks(t)

	var got *testresultsv1.ListTestResultsRequest
	listTestResultsFunc = func(_ context.Context, _, _ string, req *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		got = req
		return &testresultsv1.ListTestResultsResponse{OwnerId: req.GetOwnerId()}, nil
	}

	_, err := executeCommand("owner-1")
	if err != nil {
		t.Fatal(err)
	}

	if got.GetOwnerType() != testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_UNSPECIFIED {
		t.Fatalf("expected unspecified owner type, got %v", got.GetOwnerType())
	}
	if got.GetOwnerId() != "owner-1" {
		t.Fatalf("expected owner ID %q, got %q", "owner-1", got.GetOwnerId())
	}
}

func TestRunRejectsMultipleBackends(t *testing.T) {
	resetTestHooks(t)

	_, err := executeCommand("owner-1", "--ci", "--gha")
	if err == nil || !strings.Contains(err.Error(), "--ci and --gha are mutually exclusive") {
		t.Fatalf("expected backend selection error, got %v", err)
	}
}

func TestRunRejectsUnknownStatusBeforeAPICall(t *testing.T) {
	resetTestHooks(t)

	called := false
	listTestResultsFunc = func(context.Context, string, string, *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		called = true
		return nil, nil
	}

	_, err := executeCommand("owner-1", "--gha", "--status", "flaky")
	if err == nil || !strings.Contains(err.Error(), "unknown status") {
		t.Fatalf("expected unknown status error, got %v", err)
	}
	if called {
		t.Fatal("expected status validation to fail before API call")
	}
}

func TestRunRejectsUnknownOutputBeforeAuthOrAPICalls(t *testing.T) {
	resetTestHooks(t)

	authCalled := false
	resolveOrgAuthFunc = func(context.Context, string) (string, error) {
		authCalled = true
		return "token-1", nil
	}
	apiCalled := false
	listTestResultsFunc = func(context.Context, string, string, *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		apiCalled = true
		return nil, nil
	}

	_, err := executeCommand("owner-1", "--output", "yaml")
	if err == nil || !strings.Contains(err.Error(), "unknown output format") {
		t.Fatalf("expected unknown output error, got %v", err)
	}
	if authCalled {
		t.Fatal("expected output validation to fail before auth resolution")
	}
	if apiCalled {
		t.Fatal("expected output validation to fail before API call")
	}
}

func TestRunUsesStaticAuthForJSONOutput(t *testing.T) {
	resetTestHooks(t)

	resolveOrgAuthFunc = func(context.Context, string) (string, error) {
		t.Fatal("expected JSON output to avoid interactive auth resolution")
		return "", nil
	}
	resolveStaticAuthFunc = func(string) string {
		return "static-token"
	}

	var gotToken string
	listTestResultsFunc = func(_ context.Context, token, _ string, req *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		gotToken = token
		return &testresultsv1.ListTestResultsResponse{OwnerId: req.GetOwnerId()}, nil
	}

	_, err := executeCommand("owner-1", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "static-token" {
		t.Fatalf("expected static token, got %q", gotToken)
	}
}

func TestRunDefaultsToJSONWhenNotTerminal(t *testing.T) {
	resetTestHooks(t)

	isTerminalFunc = func() bool {
		return false
	}
	resolveStaticAuthFunc = func(string) string {
		return "static-token"
	}
	listTestResultsFunc = func(context.Context, string, string, *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		return &testresultsv1.ListTestResultsResponse{OwnerId: "owner-1"}, nil
	}

	out, err := executeCommand("owner-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"ownerId":"owner-1"`) {
		t.Fatalf("expected JSON output by default when non-terminal, got %q", out)
	}
}

func TestRunPreservesRunLookupErrorWhenAttemptLookupFails(t *testing.T) {
	resetTestHooks(t)

	ciGetRunStatusFunc = func(context.Context, string, string, string) (*civ1.GetRunStatusResponse, error) {
		return nil, errors.New("run lookup unavailable")
	}
	listTestResultsFunc = func(context.Context, string, string, *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		return nil, errors.New("attempt not found")
	}

	_, err := executeCommand("run-1", "--ci")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "as run: run lookup unavailable") {
		t.Fatalf("expected run lookup error to be preserved, got %v", err)
	}
	if !strings.Contains(err.Error(), "as attempt: attempt not found") {
		t.Fatalf("expected attempt error to be preserved, got %v", err)
	}
}

func TestWriteJSONPreservesFailureDetailsAndPageToken(t *testing.T) {
	resp := &testresultsv1.ListTestResultsResponse{
		OwnerType:     testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_CI,
		OwnerId:       "attempt-1",
		NextPageToken: "next-token",
		Results: []*testresultsv1.TestResult{
			{
				TestName:       "TestFails",
				Status:         testresultsv1.TestResultStatus_TEST_RESULT_STATUS_FAILED,
				FailureMessage: "boom",
				StackTrace:     "stack",
				SystemOut:      "stdout",
				SystemErr:      "stderr",
			},
		},
	}

	var out bytes.Buffer
	if err := writeJSON(&out, resp); err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["nextPageToken"] != "next-token" {
		t.Fatalf("expected next page token, got %v", parsed["nextPageToken"])
	}
	results := parsed["results"].([]any)
	first := results[0].(map[string]any)
	if first["failureMessage"] != "boom" || first["stackTrace"] != "stack" {
		t.Fatalf("expected failure details, got %v", first)
	}
}

func TestWriteTableEmptyResults(t *testing.T) {
	var out bytes.Buffer
	if err := writeTable(&out, &testresultsv1.ListTestResultsResponse{}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "No test results found." {
		t.Fatalf("unexpected output %q", out.String())
	}
}

func TestWriteTableSanitizesCellsAndPrintsNextPageToken(t *testing.T) {
	resp := &testresultsv1.ListTestResultsResponse{
		NextPageToken: "next\ttoken",
		Results: []*testresultsv1.TestResult{
			{
				Status:         testresultsv1.TestResultStatus_TEST_RESULT_STATUS_FAILED,
				SuiteName:      "suite\tone",
				TestName:       "Test\nName",
				FileName:       "file\rname.go",
				FailureMessage: "failed\tmessage\nsecond line",
			},
		},
	}

	var out bytes.Buffer
	if err := writeTable(&out, resp); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	for _, forbidden := range []string{"suite\tone", "Test\nName", "file\rname.go", "failed\tmessage", "next\ttoken"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("expected table output to sanitize %q, got %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "Next page token: next token") {
		t.Fatalf("expected sanitized next page token, got %q", got)
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	got := truncate(strings.Repeat("é", 100), 80)

	if !utf8.ValidString(got) {
		t.Fatalf("expected truncated value to remain valid UTF-8, got %q", got)
	}
	if len([]rune(got)) != 80 {
		t.Fatalf("expected truncated value to contain 80 runes, got %d", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected truncated value to end with ellipsis, got %q", got)
	}
}

func executeCommand(args ...string) (string, error) {
	cmd := NewCmdTests()
	var out bytes.Buffer
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

func resetTestHooks(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_WORKSPACE", "")

	resolveOrgAuthFunc = func(context.Context, string) (string, error) {
		return "token-1", nil
	}
	resolveStaticAuthFunc = func(string) string {
		return "token-1"
	}
	currentOrgFunc = func() string {
		return "org-1"
	}
	listTestResultsFunc = func(context.Context, string, string, *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		return &testresultsv1.ListTestResultsResponse{}, nil
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		return &testresultsv1.ReportTestResultsResponse{}, nil
	}
	ciGetRunStatusFunc = func(context.Context, string, string, string) (*civ1.GetRunStatusResponse, error) {
		return nil, errors.New("not found")
	}
	splitTestsFunc = func(_ context.Context, _ string, req *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return &testresultsv1.SplitTestsResponse{
			Candidates:          req.GetCandidates(),
			CandidatesRequested: uint32(len(req.GetCandidates())),
			CandidatesSelected:  uint32(len(req.GetCandidates())),
		}, nil
	}
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		return "oidc-token", nil
	}
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		return 0, nil
	}
	isTerminalFunc = func() bool {
		return true
	}
	isStdinTerminalFunc = func() bool {
		return true
	}
	oidcDebugWriter = defaultOIDCDebugWriter

	t.Cleanup(func() {
		resolveOrgAuthFunc = helpersResolveOrgAuth
		resolveStaticAuthFunc = staticResolveOrgAuth
		currentOrgFunc = configCurrentOrg
		listTestResultsFunc = apiListTestResults
		reportTestResultsFunc = apiReportTestResults
		ciGetRunStatusFunc = apiCIGetRunStatus
		splitTestsFunc = apiSplitTests
		resolveOIDCCredentialFunc = testsResolveOIDCCredential
		runShellCommandFunc = testsRunShellCommand
		isTerminalFunc = helpersIsTerminal
		isStdinTerminalFunc = helpersIsStdinTerminal
		oidcDebugWriter = defaultOIDCDebugWriter
	})
}

func equalStatuses(left, right []testresultsv1.TestResultStatus) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

var (
	helpersResolveOrgAuth      = resolveOrgAuthFunc
	staticResolveOrgAuth       = resolveStaticAuthFunc
	configCurrentOrg           = currentOrgFunc
	apiListTestResults         = listTestResultsFunc
	apiReportTestResults       = reportTestResultsFunc
	apiCIGetRunStatus          = ciGetRunStatusFunc
	apiSplitTests              = splitTestsFunc
	testsResolveOIDCCredential = resolveOIDCCredentialFunc
	testsRunShellCommand       = runShellCommandFunc
	helpersIsTerminal          = isTerminalFunc
	helpersIsStdinTerminal     = isStdinTerminalFunc
	defaultOIDCDebugWriter     = oidcDebugWriter
)
