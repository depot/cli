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

	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	if issues[0].Feature != "container" {
		t.Fatalf("expected container issue, got %q", issues[0].Feature)
	}
	if issues[0].Level != Unsupported {
		t.Fatalf("expected unsupported level, got %v", issues[0].Level)
	}
}

func TestAnalyzeJobsServicesIssue(t *testing.T) {
	jobs := []migrate.JobInfo{{Name: "integration", HasServices: true}}
	issues := AnalyzeJobs(jobs)

	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	if issues[0].Feature != "services" {
		t.Fatalf("expected services issue, got %q", issues[0].Feature)
	}
	if issues[0].Level != Unsupported {
		t.Fatalf("expected unsupported level, got %v", issues[0].Level)
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
	if got := len(report.Issues); got != 3 {
		t.Fatalf("expected three issues, got %d", got)
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
