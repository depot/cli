package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func TestDiagnoseHumanGroupedOutputWithOrgQualifiedCommands(t *testing.T) {
	restoreDiagnoseAPI(t)

	var calls int
	var capturedToken string
	var capturedOrgID string
	var capturedRequest *civ1.GetFailureDiagnosisRequest
	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		calls++
		capturedToken = token
		capturedOrgID = orgID
		capturedRequest = req
		return groupedDiagnosisResponse(true), nil
	}

	stdout, stderr, err := executeDiagnoseTextCommand([]string{"--org", "org-123", "--token", "token-123", "--run", "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if calls != 1 {
		t.Fatalf("ciDiagnose calls = %d, want 1", calls)
	}
	if capturedToken != "token-123" || capturedOrgID != "org-123" {
		t.Fatalf("token/org = %q/%q, want token-123/org-123", capturedToken, capturedOrgID)
	}
	if capturedRequest.GetTargetId() != "run-1" || capturedRequest.GetTargetType() != civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_RUN {
		t.Fatalf("unexpected request: %+v", capturedRequest)
	}

	for _, want := range []string{
		"Target: run run-1 (failed)",
		"Source: depot/cli @ refs/heads/main (abc123)",
		"Failure groups: 2 (1 omitted)",
		"Group 1: go test ./... failed",
		"3 failures",
		"Diagnosis:\n    Unit tests failed in package pkg/cmd/ci.",
		"Possible fix:\n    Fix the failing assertion and rerun tests.",
		"Attempts:",
		"- #2 att-1  ci.yml:test (failed)",
		"Evidence:",
		"build:42: expected true, got false",
		"Logs: depot ci logs att-1 --org org-123",
		"Summary: depot ci summary att-1 --org org-123",
		"View: https://depot.dev/orgs/org-123/workflows/workflow-1?job=job-1&attempt=att-1",
		"Showing 1 of 3 similar attempts for this group.",
		"Omitted failure groups: 1; run a narrower diagnose command for details.",
		"Output was truncated by diagnosis bounds.",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("diagnose output missing %q:\n%s", want, stdout)
		}
	}
	if count := strings.Count(stdout, "Unit tests failed in package pkg/cmd/ci."); count != 1 {
		t.Fatalf("diagnosis rendered %d times, want group-level only:\n%s", count, stdout)
	}
	if count := strings.Count(stdout, "Fix the failing assertion and rerun tests."); count != 1 {
		t.Fatalf("possible fix rendered %d times, want group-level only:\n%s", count, stdout)
	}
	if count := strings.Count(stdout, "go test ./... failed"); count != 1 {
		t.Fatalf("group error rendered %d times, want group heading only:\n%s", count, stdout)
	}
}

func TestDiagnoseRepresentativeSamplingDoesNotPrintGenericTruncationFooter(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		resp := groupedDiagnosisResponse(true)
		resp.Bounds.TotalFailureGroupCount = 1
		resp.Bounds.OmittedFailureGroupCount = 0
		resp.Bounds.Truncated = false
		resp.FailureGroups[0].Count = 7
		resp.FailureGroups[0].Representatives = []*civ1.RepresentativeAttempt{
			diagnoseRepresentative(true),
			diagnoseRepresentative(true),
			diagnoseRepresentative(true),
		}
		resp.FailureGroups[0].OmittedRepresentativeCount = 4
		return resp, nil
	}

	stdout, _, err := executeDiagnoseTextCommand([]string{"--org", "org-123", "--token", "token-123", "--run", "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Showing 3 of 7 similar attempts for this group.") {
		t.Fatalf("diagnose output missing representative sampling summary:\n%s", stdout)
	}
	if strings.Contains(stdout, "Output was truncated by diagnosis bounds.") {
		t.Fatalf("diagnose output included generic truncation footer for representative sampling only:\n%s", stdout)
	}
}

func TestDiagnoseRepresentativeSamplingStillPrintsRealTruncationFooter(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		resp := groupedDiagnosisResponse(true)
		resp.Bounds.TotalFailureGroupCount = 1
		resp.Bounds.OmittedFailureGroupCount = 0
		resp.Bounds.Truncated = true
		resp.FailureGroups[0].Count = 7
		resp.FailureGroups[0].Representatives = []*civ1.RepresentativeAttempt{
			diagnoseRepresentative(true),
			diagnoseRepresentative(true),
			diagnoseRepresentative(true),
		}
		resp.FailureGroups[0].OmittedRepresentativeCount = 4
		return resp, nil
	}

	stdout, _, err := executeDiagnoseTextCommand([]string{"--org", "org-123", "--token", "token-123", "--run", "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Showing 3 of 7 similar attempts for this group.") {
		t.Fatalf("diagnose output missing representative sampling summary:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Output was truncated by diagnosis bounds.") {
		t.Fatalf("diagnose output missing generic truncation footer when bounds truncated is true:\n%s", stdout)
	}
}

func TestDiagnoseHumanEvidenceOnlySuppressesStableWrapperLine(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		resp := groupedDiagnosisResponse(true)
		resp.FailureGroups[0].Representatives[0].RelevantLines = []*civ1.RelevantErrorLine{
			{StepId: "test", LineNumber: 10, Content: "##[error]script exited with code 1"},
			{StepId: "test", LineNumber: 11, Content: "ERR_PNPM_RECURSIVE_RUN_FIRST_FAIL package failed"},
			{StepId: "test", LineNumber: 12, Content: "ELIFECYCLE Command failed with exit code 1"},
		}
		return resp, nil
	}

	stdout, _, err := executeDiagnoseTextCommand([]string{"--org", "org-123", "--token", "token-123", "--run", "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "script exited with code 1") {
		t.Fatalf("stable wrapper line was not suppressed:\n%s", stdout)
	}
	for _, want := range []string{
		"ERR_PNPM_RECURSIVE_RUN_FIRST_FAIL package failed",
		"ELIFECYCLE Command failed with exit code 1",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human evidence missing %q:\n%s", want, stdout)
		}
	}
}

func TestDiagnoseHumanGroupedOutputShowsDifferingRepresentativeErrorsWhenGroupErrorIsEmpty(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		resp := groupedDiagnosisResponse(false)
		first := diagnoseRepresentative(false)
		first.AttemptId = "att-1"
		first.Attempt = 1
		first.ErrorMessage = "first representative failed"
		second := diagnoseRepresentative(false)
		second.AttemptId = "att-2"
		second.Attempt = 2
		second.ErrorMessage = "second representative failed"
		resp.FailureGroups[0].ErrorMessage = ""
		resp.FailureGroups[0].Representatives = []*civ1.RepresentativeAttempt{first, second}
		resp.FailureGroups[0].Count = 2
		resp.FailureGroups[0].OmittedRepresentativeCount = 0
		return resp, nil
	}

	stdout, _, err := executeDiagnoseTextCommand([]string{"--org", "org-123", "--token", "token-123", "--run", "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Error: first representative failed",
		"Error: second representative failed",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("diagnose output missing representative error %q:\n%s", want, stdout)
		}
	}
}

func TestDiagnoseGroupedOutputDoesNotRepeatRepresentativeCommandsFooter(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		resp := groupedDiagnosisResponse(true)
		resp.NextCommands = []*civ1.DrillDownCommand{logsCommand("att-1"), summaryCommand("att-1")}
		return resp, nil
	}

	stdout, _, err := executeDiagnoseTextCommand([]string{"--org", "org-123", "--token", "token-123", "--run", "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "Next commands:") {
		t.Fatalf("grouped output repeated representative commands in footer:\n%s", stdout)
	}
	if count := strings.Count(stdout, "depot ci logs att-1 --org org-123"); count != 1 {
		t.Fatalf("logs command rendered %d times, want once:\n%s", count, stdout)
	}
	if count := strings.Count(stdout, "depot ci summary att-1 --org org-123"); count != 1 {
		t.Fatalf("summary command rendered %d times, want once:\n%s", count, stdout)
	}
}

func TestDiagnoseFocusedOutputDoesNotRepeatRepresentativeCommandsFooter(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		resp := focusedDiagnosisResponse(true)
		resp.NextCommands = []*civ1.DrillDownCommand{logsCommand("att-1"), summaryCommand("att-1")}
		return resp, nil
	}

	stdout, _, err := executeDiagnoseTextCommand([]string{"--org", "org-123", "--token", "token-123", "--attempt", "att-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "Next commands:") {
		t.Fatalf("focused output repeated representative commands in footer:\n%s", stdout)
	}
	if count := strings.Count(stdout, "depot ci logs att-1 --org org-123"); count != 1 {
		t.Fatalf("logs command rendered %d times, want once:\n%s", count, stdout)
	}
	if count := strings.Count(stdout, "depot ci summary att-1 --org org-123"); count != 1 {
		t.Fatalf("summary command rendered %d times, want once:\n%s", count, stdout)
	}
}

func TestDiagnoseFocusedTextIgnoresFailureGroups(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		return focusedDiagnosisResponseWithGroup(true), nil
	}

	stdout, _, err := executeDiagnoseTextCommand([]string{"--org", "org-123", "--token", "token-123", "--attempt", "att-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Focused diagnosis:") {
		t.Fatalf("focused output missing focused diagnosis:\n%s", stdout)
	}
	if strings.Contains(stdout, "Failure groups:") || strings.Contains(stdout, "Group 1:") {
		t.Fatalf("focused output rendered failure groups:\n%s", stdout)
	}
	if count := strings.Count(stdout, "Unit tests failed in package pkg/cmd/ci."); count != 1 {
		t.Fatalf("focused diagnosis rendered %d times, want once:\n%s", count, stdout)
	}
	if strings.Contains(stdout, "group-level diagnosis should stay JSON-only") {
		t.Fatalf("focused text output leaked group diagnosis:\n%s", stdout)
	}
}

func TestDiagnoseJSONOutputIsCLINormalized(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		return groupedDiagnosisResponse(true), nil
	}

	cmd := NewCmdDiagnose()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output", "json", "--run", "run-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "FAILURE_DIAGNOSIS_STATE") || strings.Contains(stdout, "DRILL_DOWN_COMMAND_KIND") {
		t.Fatalf("json output leaked raw protobuf enum names:\n%s", stdout)
	}

	var got diagnoseJSONDocument
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
	if got.OrgID != "org-123" || got.State != "grouped_failures" {
		t.Fatalf("unexpected top-level JSON: %+v", got)
	}
	if got.Target.TargetType != "run" || got.Context.JobKey != "ci.yml:test" {
		t.Fatalf("unexpected target/context JSON: %+v %+v", got.Target, got.Context)
	}
	if got.Target.Status != "failed" || got.Context.JobStatus != "failed" || got.Context.JobConclusion != "failure" {
		t.Fatalf("unexpected status/conclusion JSON: %+v %+v", got.Target, got.Context)
	}
	if got.Context.TruncatedContextFields == nil {
		t.Fatalf("truncated_context_fields should decode as a non-nil empty slice:\n%s", stdout)
	}
	if !got.CommandCapabilities.SummaryCommandAvailable {
		t.Fatalf("summary capability = false, want true")
	}
	if got.Bounds.FailedProblemCandidateCount != 3 || got.Bounds.OmittedFailureGroupCount != 1 || !got.Bounds.Truncated {
		t.Fatalf("unexpected bounds JSON: %+v", got.Bounds)
	}
	if len(got.FailureGroups) != 1 || len(got.FailureGroups[0].Representatives) != 1 {
		t.Fatalf("unexpected failure groups JSON: %+v", got.FailureGroups)
	}
	commands := got.FailureGroups[0].Representatives[0].NextCommands
	if len(commands) != 2 {
		t.Fatalf("commands = %+v, want logs and summary", commands)
	}
	if strings.Contains(stdout, `"command"`) {
		t.Fatalf("json output included command string; argv should be the machine-safe command representation:\n%s", stdout)
	}
	if commands[0].Kind != "logs" || strings.Join(commands[0].Argv, " ") != "depot ci logs att-1 --org org-123" {
		t.Fatalf("unexpected logs command JSON: %+v", commands[0])
	}
	if commands[1].Kind != "summary" || strings.Join(commands[1].Argv, " ") != "depot ci summary att-1 --org org-123" {
		t.Fatalf("unexpected summary command JSON: %+v", commands[1])
	}
}

func TestDiagnoseFocusedJSONKeepsFailureGroups(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		return focusedDiagnosisResponseWithGroup(true), nil
	}

	cmd := NewCmdDiagnose()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--attempt", "att-1", "--output", "json"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}

	var got diagnoseJSONDocument
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
	if got.State != "focused_failure" {
		t.Fatalf("state = %q, want focused_failure", got.State)
	}
	if len(got.RepresentativeAttempts) != 1 {
		t.Fatalf("representative_attempts = %+v, want one", got.RepresentativeAttempts)
	}
	if len(got.FailureGroups) != 1 {
		t.Fatalf("failure_groups = %+v, want API groups preserved in JSON", got.FailureGroups)
	}
	if got.FailureGroups[0].Diagnosis != "group-level diagnosis should stay JSON-only" {
		t.Fatalf("failure group diagnosis = %q", got.FailureGroups[0].Diagnosis)
	}
}

func TestDiagnoseOmitsSummaryCommandsWhenUnavailable(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		return focusedDiagnosisResponse(false), nil
	}

	stdout, _, err := executeDiagnoseTextCommand([]string{"--org", "org-123", "--token", "token-123", "--attempt", "att-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "depot ci summary") {
		t.Fatalf("summary command rendered when unavailable:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Summary drill-down commands are not available in this build.") {
		t.Fatalf("missing summary unavailable note:\n%s", stdout)
	}

	cmd := NewCmdDiagnose()
	cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--attempt", "att-1", "--output", "json"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	jsonStdout, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	var got diagnoseJSONDocument
	if err := json.Unmarshal([]byte(jsonStdout), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, jsonStdout)
	}
	if got.CommandCapabilities.SummaryCommandAvailable {
		t.Fatalf("summary capability = true, want false")
	}
	if len(got.RepresentativeAttempts) != 1 {
		t.Fatalf("representative_attempts = %+v", got.RepresentativeAttempts)
	}
	for _, command := range got.RepresentativeAttempts[0].NextCommands {
		if command.Kind == "summary" || strings.Contains(strings.Join(command.Argv, " "), "summary") {
			t.Fatalf("summary command should be omitted: %+v", command)
		}
	}
}

func TestDiagnoseCommandsDoNotIncludeOrgWhenFlagOmitted(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		return focusedDiagnosisResponse(true), nil
	}

	stdout, _, err := executeDiagnoseTextCommand([]string{"--token", "token-123", "--attempt", "att-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Logs: depot ci logs att-1\n") {
		t.Fatalf("logs command without org flag missing:\n%s", stdout)
	}
	if strings.Contains(stdout, "--org") {
		t.Fatalf("org flag rendered despite not being user-supplied:\n%s", stdout)
	}
}

func TestDiagnoseEmptyStateIsNonError(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		return &civ1.GetFailureDiagnosisResponse{
			OrgId:       "org-123",
			Target:      &civ1.FailureDiagnosisTarget{TargetId: "run-1", TargetType: civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_RUN, Status: civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FINISHED},
			State:       civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_EMPTY,
			EmptyReason: civ1.FailureDiagnosisEmptyReason_FAILURE_DIAGNOSIS_EMPTY_REASON_NO_FAILURE_EVIDENCE,
			Bounds:      &civ1.FailureDiagnosisBounds{TotalProblemJobCount: 0},
		}, nil
	}

	stdout, stderr, err := executeDiagnoseTextCommand([]string{"--org", "org-123", "--token", "token-123", "--run", "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "No CI failures found for this target.") || !strings.Contains(stdout, "Reason: no_failure_evidence") {
		t.Fatalf("empty output missing expected message:\n%s", stdout)
	}
}

func TestDiagnoseOverLimitStateRendersBreakdown(t *testing.T) {
	restoreDiagnoseAPI(t)

	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		return overLimitDiagnosisResponse(), nil
	}

	stdout, _, err := executeDiagnoseTextCommand([]string{"--org", "org-123", "--token", "token-123", "--run", "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Diagnosis is over limit.",
		"Failed/problem candidates: 650 (cap 512)",
		"workflow workflow-1 [ci.yml] (failed): 500 failed/problem candidates",
		"Diagnose: depot ci diagnose --workflow workflow-1 --org org-123",
		"Omitted job breakdown rows: 3.",
		"Output was truncated by diagnosis bounds.",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("over-limit output missing %q:\n%s", want, stdout)
		}
	}
}

func TestDiagnoseSelectorParsingAndInvalidFlagsBeforeAPI(t *testing.T) {
	tests := []struct {
		name       string
		runID      string
		workflowID string
		jobID      string
		attemptID  string
		wantID     string
		wantType   civ1.FailureDiagnosisTargetType
	}{
		{name: "run", runID: "run-1", wantID: "run-1", wantType: civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_RUN},
		{name: "workflow", workflowID: "workflow-1", wantID: "workflow-1", wantType: civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_WORKFLOW},
		{name: "job", jobID: "job-1", wantID: "job-1", wantType: civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_JOB},
		{name: "attempt", attemptID: "att-1", wantID: "att-1", wantType: civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_ATTEMPT},
	}
	for _, tt := range tests {
		gotID, gotType, err := diagnosisTargetFromSelectors(tt.runID, tt.workflowID, tt.jobID, tt.attemptID)
		if err != nil {
			t.Fatalf("%s returned error: %v", tt.name, err)
		}
		if gotID != tt.wantID || gotType != tt.wantType {
			t.Fatalf("%s = %q/%v, want %q/%v", tt.name, gotID, gotType, tt.wantID, tt.wantType)
		}
	}
	if _, _, err := diagnosisTargetFromSelectors("", "", "", ""); err == nil || !strings.Contains(err.Error(), "expected exactly one target selector") {
		t.Fatalf("missing selector error = %v", err)
	}
	if _, _, err := diagnosisTargetFromSelectors("run-1", "", "job-1", ""); err == nil || !strings.Contains(err.Error(), "expected exactly one target selector") {
		t.Fatalf("multiple selector error = %v", err)
	}

	restoreDiagnoseAPI(t)
	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		t.Fatal("diagnose API should not be called for invalid flags")
		return nil, nil
	}

	cmd := NewCmdDiagnose()
	cmd.SetArgs([]string{"--token", "token-123", "--run", "run-1", "--job", "job-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "expected exactly one target selector") {
		t.Fatalf("invalid selector command error = %v", err)
	}

	cmd = NewCmdDiagnose()
	cmd.SetArgs([]string{"--token", "token-123", "run-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "positional target IDs are not supported") {
		t.Fatalf("positional target command error = %v", err)
	}

	cmd = NewCmdDiagnose()
	cmd.SetArgs([]string{"--token", "token-123"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "expected exactly one target selector") {
		t.Fatalf("missing selector command error = %v", err)
	}

	cmd = NewCmdDiagnose()
	cmd.SetArgs([]string{"--token", "token-123", "--output", "yaml", "--run", "run-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), `unsupported output "yaml"`) {
		t.Fatalf("invalid output command error = %v", err)
	}
}

func TestDiagnoseAPIErrorDoesNotProbeOtherAPIs(t *testing.T) {
	restoreDiagnoseAPI(t)

	var calls int
	ciDiagnose = func(ctx context.Context, token, orgID string, req *civ1.GetFailureDiagnosisRequest) (*civ1.GetFailureDiagnosisResponse, error) {
		calls++
		return nil, connect.NewError(connect.CodeNotFound, errors.New("not found"))
	}

	_, _, err := executeDiagnoseTextCommand([]string{"--org", "org-123", "--token", "token-123", "--run", "missing-id"})
	if err == nil || !strings.Contains(err.Error(), "failed to diagnose CI target") {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Fatalf("ciDiagnose calls = %d, want 1", calls)
	}
}

func executeDiagnoseTextCommand(args []string) (string, string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewCmdDiagnose()
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func restoreDiagnoseAPI(t *testing.T) {
	t.Helper()

	originalDiagnose := ciDiagnose
	t.Cleanup(func() {
		ciDiagnose = originalDiagnose
	})
}

func groupedDiagnosisResponse(summaryAvailable bool) *civ1.GetFailureDiagnosisResponse {
	return &civ1.GetFailureDiagnosisResponse{
		OrgId: "org-123",
		Target: &civ1.FailureDiagnosisTarget{
			TargetId:   "run-1",
			TargetType: civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_RUN,
			Status:     civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
		},
		Context: &civ1.FailureDiagnosisContext{
			RunId:          "run-1",
			Repo:           "depot/cli",
			Ref:            "refs/heads/main",
			Sha:            "abc123",
			RunStatus:      civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
			WorkflowId:     "workflow-1",
			WorkflowName:   "CI",
			WorkflowPath:   ".depot/workflows/ci.yml",
			WorkflowStatus: civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
			JobId:          "job-1",
			JobKey:         "ci.yml:test",
			JobStatus:      civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
			JobConclusion:  civ1.FailureDiagnosisConclusion_FAILURE_DIAGNOSIS_CONCLUSION_FAILURE,
			AttemptId:      "att-1",
			Attempt:        2,
			AttemptStatus:  civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
		},
		State: civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_GROUPED_FAILURES,
		FailureGroups: []*civ1.FailureGroup{
			{
				Fingerprint:                "attempt_error:go-test",
				Source:                     "attempt_error",
				Count:                      3,
				ErrorMessage:               "go test ./... failed",
				ErrorMessageTruncated:      false,
				ErrorMessageOriginalLength: 20,
				Diagnosis:                  "Unit tests failed in package pkg/cmd/ci.",
				PossibleFix:                "Fix the failing assertion and rerun tests.",
				Representatives: []*civ1.RepresentativeAttempt{
					diagnoseRepresentative(summaryAvailable),
				},
				OmittedRepresentativeCount: 2,
			},
		},
		Bounds: &civ1.FailureDiagnosisBounds{
			FailedProblemCandidateCount:  3,
			FailedProblemCandidateCap:    512,
			TotalProblemJobCount:         3,
			TotalFailureGroupCount:       2,
			OmittedFailureGroupCount:     1,
			FailureGroupLimit:            20,
			RepresentativesPerGroupLimit: 3,
			RelevantLineLimit:            10,
			ErrorLineBodyCharLimit:       8000,
			ErrorMessageCharLimit:        2000,
			ContextLabelCharLimit:        255,
			Truncated:                    true,
		},
		CommandCapabilities: &civ1.FailureDiagnosisCommandCapabilities{SummaryCommandAvailable: summaryAvailable},
	}
}

func focusedDiagnosisResponse(summaryAvailable bool) *civ1.GetFailureDiagnosisResponse {
	return &civ1.GetFailureDiagnosisResponse{
		OrgId: "org-123",
		Target: &civ1.FailureDiagnosisTarget{
			TargetId:   "att-1",
			TargetType: civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_ATTEMPT,
			Status:     civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
		},
		Context: &civ1.FailureDiagnosisContext{
			RunId:          "run-1",
			Repo:           "depot/cli",
			Ref:            "refs/heads/main",
			Sha:            "abc123",
			WorkflowId:     "workflow-1",
			WorkflowStatus: civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
			JobId:          "job-1",
			JobKey:         "ci.yml:test",
			JobStatus:      civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
			AttemptId:      "att-1",
			Attempt:        2,
			AttemptStatus:  civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
		},
		State:                  civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_FOCUSED_FAILURE,
		RepresentativeAttempts: []*civ1.RepresentativeAttempt{diagnoseRepresentative(summaryAvailable)},
		CommandCapabilities:    &civ1.FailureDiagnosisCommandCapabilities{SummaryCommandAvailable: summaryAvailable},
		Bounds:                 &civ1.FailureDiagnosisBounds{TotalAttemptCount: 1, RecentAttemptLimit: 5},
	}
}

func focusedDiagnosisResponseWithGroup(summaryAvailable bool) *civ1.GetFailureDiagnosisResponse {
	resp := focusedDiagnosisResponse(summaryAvailable)
	resp.FailureGroups = []*civ1.FailureGroup{
		{
			Fingerprint:  "attempt_error:go-test",
			Source:       "attempt_error",
			Count:        1,
			ErrorMessage: "go test ./... failed",
			Diagnosis:    "group-level diagnosis should stay JSON-only",
			PossibleFix:  "group-level fix should stay JSON-only",
			Representatives: []*civ1.RepresentativeAttempt{
				diagnoseRepresentative(summaryAvailable),
			},
		},
	}
	resp.Bounds.TotalFailureGroupCount = 1
	return resp
}

func diagnoseRepresentative(summaryAvailable bool) *civ1.RepresentativeAttempt {
	commands := []*civ1.DrillDownCommand{logsCommand("att-1")}
	if summaryAvailable {
		commands = append(commands, summaryCommand("att-1"))
	}
	return &civ1.RepresentativeAttempt{
		RunId:                      "run-1",
		WorkflowId:                 "workflow-1",
		WorkflowName:               "CI",
		WorkflowPath:               ".depot/workflows/ci.yml",
		JobId:                      "job-1",
		JobKey:                     "ci.yml:test",
		JobStatus:                  civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
		JobConclusion:              civ1.FailureDiagnosisConclusion_FAILURE_DIAGNOSIS_CONCLUSION_FAILURE,
		AttemptId:                  "att-1",
		Attempt:                    2,
		AttemptStatus:              civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
		AttemptConclusion:          civ1.FailureDiagnosisConclusion_FAILURE_DIAGNOSIS_CONCLUSION_FAILURE,
		ErrorMessage:               "go test ./... failed",
		ErrorMessageOriginalLength: 20,
		Diagnosis:                  "Unit tests failed in package pkg/cmd/ci.",
		PossibleFix:                "Fix the failing assertion and rerun tests.",
		RelevantLines: []*civ1.RelevantErrorLine{
			{
				StepId:     "build",
				LineNumber: 42,
				Content:    "expected true, got false",
			},
		},
		NextCommands: commands,
	}
}

func overLimitDiagnosisResponse() *civ1.GetFailureDiagnosisResponse {
	return &civ1.GetFailureDiagnosisResponse{
		OrgId: "org-123",
		Target: &civ1.FailureDiagnosisTarget{
			TargetId:   "run-1",
			TargetType: civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_RUN,
			Status:     civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
		},
		Context: &civ1.FailureDiagnosisContext{
			RunId:     "run-1",
			Repo:      "depot/cli",
			Ref:       "refs/heads/main",
			Sha:       "abc123",
			RunStatus: civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
		},
		State: civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_OVER_LIMIT,
		Bounds: &civ1.FailureDiagnosisBounds{
			FailedProblemCandidateCount:     650,
			FailedProblemCandidateCap:       512,
			OverLimitWorkflowBreakdownLimit: 25,
			OverLimitJobBreakdownLimit:      25,
			OmittedJobBreakdownCount:        3,
			Truncated:                       true,
		},
		OverLimitBreakdown: []*civ1.FailureDiagnosisBreakdownRow{
			{
				TargetType:                  civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_WORKFLOW,
				TargetId:                    "workflow-1",
				Label:                       "ci.yml",
				Status:                      civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED,
				FailedProblemCandidateCount: 500,
				NextCommands: []*civ1.DrillDownCommand{
					{
						Kind:     civ1.DrillDownCommandKind_DRILL_DOWN_COMMAND_KIND_DIAGNOSE_WORKFLOW,
						Argv:     []string{"depot", "ci", "diagnose", "--workflow", "workflow-1"},
						TargetId: "workflow-1",
						Label:    "Diagnose",
					},
				},
			},
		},
		CommandCapabilities: &civ1.FailureDiagnosisCommandCapabilities{SummaryCommandAvailable: true},
	}
}

func logsCommand(attemptID string) *civ1.DrillDownCommand {
	return &civ1.DrillDownCommand{
		Kind:     civ1.DrillDownCommandKind_DRILL_DOWN_COMMAND_KIND_LOGS,
		Argv:     []string{"depot", "ci", "logs", attemptID},
		TargetId: attemptID,
		Label:    "Logs",
	}
}

func summaryCommand(attemptID string) *civ1.DrillDownCommand {
	return &civ1.DrillDownCommand{
		Kind:     civ1.DrillDownCommandKind_DRILL_DOWN_COMMAND_KIND_SUMMARY,
		Argv:     []string{"depot", "ci", "summary", attemptID},
		TargetId: attemptID,
		Label:    "Summary",
	}
}
