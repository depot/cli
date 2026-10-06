package compat

import (
	"strings"
	"testing"

	"github.com/depot/cli/pkg/ci/migrate"
)

func TestAnalyzeWorkflowSupportedTriggersOnly(t *testing.T) {
	workflow := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Triggers: []string{"push", "pull_request"},
	}

	report := AnalyzeWorkflow(workflow)
	if got := len(report.Issues); got != 0 {
		t.Fatalf("expected zero issues, got %d", got)
	}
}

func TestAnalyzeWorkflowIssueCommentSupported(t *testing.T) {
	workflow := &migrate.WorkflowFile{
		Path:     ".github/workflows/comment.yml",
		Triggers: []string{"issue_comment"},
	}

	report := AnalyzeWorkflow(workflow)
	if len(report.Issues) != 0 {
		t.Fatalf("expected issue_comment to have no compatibility issues, got %v", report.Issues)
	}
}

func TestAnalyzeWorkflowUnsupportedReleaseTrigger(t *testing.T) {
	workflow := &migrate.WorkflowFile{
		Path:     ".github/workflows/release.yml",
		Triggers: []string{"release"},
	}

	report := AnalyzeWorkflow(workflow)
	if len(report.Issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(report.Issues))
	}

	issue := report.Issues[0]
	if issue.Level != Unsupported {
		t.Fatalf("expected unsupported level, got %v", issue.Level)
	}
	if strings.TrimSpace(issue.Suggestion) == "" {
		t.Fatal("expected non-empty suggestion")
	}
}

func TestAnalyzeJobsContainerIssue(t *testing.T) {
	jobs := []migrate.JobInfo{{Name: "build", HasContainer: true}}
	issues := AnalyzeJobs(jobs)

	if len(issues) != 0 {
		t.Fatalf("expected no issues for supported container jobs, got %d", len(issues))
	}
}

func TestAnalyzeJobsServicesIssue(t *testing.T) {
	jobs := []migrate.JobInfo{{Name: "integration", HasServices: true}}
	issues := AnalyzeJobs(jobs)

	if len(issues) != 0 {
		t.Fatalf("expected no issues for supported service containers, got %d", len(issues))
	}
}

func TestAnalyzeWorkflowMixedFeatures(t *testing.T) {
	workflow := &migrate.WorkflowFile{
		Path:     ".github/workflows/mixed.yml",
		Triggers: []string{"push", "release"},
		Jobs: []migrate.JobInfo{
			{Name: "test", HasContainer: true},
			{Name: "integration", HasServices: true},
		},
	}

	report := AnalyzeWorkflow(workflow)
	if got := len(report.Issues); got != 1 {
		t.Fatalf("expected one issue, got %d", got)
	}
}

func TestAnalyzeJobsMatrixSelfHostedUnsupported(t *testing.T) {
	jobs := []migrate.JobInfo{
		{
			Name:      "test",
			RunsOn:    "ubuntu-latest,self-hosted",
			HasMatrix: true,
		},
	}

	issues := AnalyzeJobs(jobs)
	if len(issues) != 2 {
		t.Fatalf("expected two issues (matrix+self-hosted and custom runs-on), got %d", len(issues))
	}

	var foundMatrixSelfHosted bool
	for _, issue := range issues {
		if issue.Feature == "strategy.matrix + self-hosted" {
			foundMatrixSelfHosted = true
			if issue.Level != Unsupported {
				t.Fatalf("expected unsupported level, got %v", issue.Level)
			}
		}
	}
	if !foundMatrixSelfHosted {
		t.Fatal("expected strategy.matrix + self-hosted issue")
	}
}

func TestAnalyzeUnknownTriggerDoesNotWarn(t *testing.T) {
	workflow := &migrate.WorkflowFile{
		Path:     ".github/workflows/custom.yml",
		Triggers: []string{"future_event"},
	}

	report := AnalyzeWorkflow(workflow)
	if got := len(report.Issues); got != 0 {
		t.Fatalf("expected no issues for unknown trigger, got %d", got)
	}
}

func TestUnsupportedTriggerRulesHaveSuggestions(t *testing.T) {
	for trigger, rule := range TriggerRules {
		if rule.Supported != Unsupported {
			continue
		}

		if strings.TrimSpace(rule.Suggestion) == "" {
			t.Fatalf("trigger %q has unsupported level without suggestion", trigger)
		}
	}
}

func TestSummarizeReport(t *testing.T) {
	report := &CompatibilityReport{
		Issues: []CompatibilityIssue{
			{Level: Unsupported},
			{Level: Unsupported},
			{Level: Partial},
		},
	}

	summary := SummarizeReport(report)
	if !strings.Contains(summary, "3 issues found") {
		t.Fatalf("expected count in summary, got %q", summary)
	}
	if !strings.Contains(summary, "2 unsupported") {
		t.Fatalf("expected unsupported count in summary, got %q", summary)
	}
	if !strings.Contains(summary, "1 partial") {
		t.Fatalf("expected partial count in summary, got %q", summary)
	}
}

func TestHasCriticalIssues(t *testing.T) {
	withCritical := &CompatibilityReport{
		Issues: []CompatibilityIssue{{Level: Unsupported}},
	}
	if !HasCriticalIssues(withCritical) {
		t.Fatal("expected critical issues to be true")
	}

	withoutCritical := &CompatibilityReport{
		Issues: []CompatibilityIssue{{Level: Supported}, {Level: Partial}},
	}
	if HasCriticalIssues(withoutCritical) {
		t.Fatal("expected critical issues to be false")
	}
}

func TestAnalyzeJobsRunnerLabels(t *testing.T) {
	cases := []struct {
		name       string
		runsOn     string
		wantCustom bool
	}{
		{"empty", "", false},
		{"latest", "ubuntu-latest", false},
		{"ubuntu24", "ubuntu-24.04", false},
		{"ubuntu22", "ubuntu-22.04", false},
		{"depotLatest", "depot-ubuntu-latest", false},
		{"depotSized", "depot-ubuntu-latest-16", false},
		{"depotArm", "depot-ubuntu-24.04-arm", false},
		{"caseWhitespace", "  DEPOT-UBUNTU-LATEST , Ubuntu-24.04 ", false},
		{"expression", "${{ matrix.runner }}", false},
		{"supportedList", "ubuntu-22.04,depot-ubuntu-latest", false},
		{"unknown", "custom-runner", true},
		{"windows", "windows-latest", true},
		{"macos", "macos-latest", true},
		{"selfHosted", "ubuntu-latest,self-hosted", true},
		{"mixed", "depot-ubuntu-latest,custom-runner", true},
		{"expressionMixed", "${{ matrix.runner }},custom-runner", true},
		{"legacyUnderscore", "depot_ubuntu_latest", true},
		// Depot CI sandboxes are Linux-only; depot macOS/Windows labels belong to
		// Depot GitHub Actions runners, not Depot CI.
		{"depotMacos", "depot-macos-latest", true},
		{"depotWindows", "depot-windows-latest", true},
		{"depotMacosVersion", "depot-macos-15", true},
		{"depotWindowsVersionSized", "depot-windows-2022-8", true},
		{"depotMacosCaseWhitespace", "  DEPOT-MACOS-LATEST ", true},
		{"depotWindowsMixed", "depot-ubuntu-latest,depot-windows-latest", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := AnalyzeJobs([]migrate.JobInfo{{Name: "build", RunsOn: tc.runsOn}})
			want := 0
			if tc.wantCustom {
				want = 1
			}
			if len(issues) != want {
				t.Fatalf("AnalyzeJobs(%q) = %v, want %d issues", tc.runsOn, issues, want)
			}
			if tc.wantCustom && (issues[0].Feature != "runs-on (custom labels)" || issues[0].Level != Partial) {
				t.Fatalf("unexpected custom runner issue: %v", issues[0])
			}
		})
	}
}

func TestAnalyzeJobsUnavailableRunnerLabels(t *testing.T) {
	cases := []struct {
		name         string
		runsOn       string
		wantFeatures []string
	}{
		{"ubuntu20", "ubuntu-20.04", []string{"runs-on (unavailable labels)"}},
		{"caseWhitespace", " Ubuntu-20.04 ", []string{"runs-on (unavailable labels)"}},
		{"supportedList", "ubuntu-22.04,ubuntu-20.04", []string{"runs-on (unavailable labels)"}},
		{"customList", "ubuntu-20.04,custom-runner", []string{"runs-on (custom labels)", "runs-on (unavailable labels)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := AnalyzeJobs([]migrate.JobInfo{{Name: "build", RunsOn: tc.runsOn}})
			if len(issues) != len(tc.wantFeatures) {
				t.Fatalf("AnalyzeJobs(%q) = %v, want features %v", tc.runsOn, issues, tc.wantFeatures)
			}
			for i, issue := range issues {
				if issue.Feature != tc.wantFeatures[i] || issue.Level != Partial {
					t.Fatalf("issue %d = %v, want Partial %q", i, issue, tc.wantFeatures[i])
				}
			}
			last := issues[len(issues)-1]
			if !strings.Contains(last.Message, `"ubuntu-20.04"`) || !strings.Contains(last.Suggestion, "Choose a supported Depot runner label") {
				t.Fatalf("unavailable runner issue does not ask for a runner choice: %v", last)
			}
		})
	}
}
