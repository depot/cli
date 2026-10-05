package compat

import (
	"os"
	"path/filepath"
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

func TestAnalyzeWorkflowRunnerLabels(t *testing.T) {
	cases := []struct {
		name       string
		runsOn     string // YAML value of the job's runs-on
		wantCustom bool
	}{
		{"githubLatest", "ubuntu-latest", false},
		{"githubVersion", "ubuntu-24.04", false},
		{"githubCaseWhitespace", "'  Ubuntu-22.04 '", false},
		{"depotLatest", "depot-ubuntu-latest", false},
		{"depotSmall", "depot-ubuntu-22.04-small", false},
		{"depotTwoCPU", "depot-ubuntu-24.04-2", false},
		{"depotIO", "depot-ubuntu-latest-16-io", false},
		{"depotGPU", "depot-ubuntu-22.04-8-gpu", false},
		{"depotArmSized", "depot-ubuntu-24.04-arm-64", false},
		{"depot96CPU", "depot-ubuntu-latest-96", true},
		{"depot192CPU", "depot-ubuntu-24.04-arm-192", true},
		{"depotMacos", "depot-macos-latest", true},
		{"depotWindowsSized", "depot-windows-2022-8", true},
		{"githubWindows", "windows-latest", true},
		{"unknown", "custom-runner", true},
		{"legacyUnderscore", "depot_ubuntu_latest", true},
		{"depotUnknownAlone", "depot-made-up", true},
		{"depotUndocumentedSize", "depot-ubuntu-24.04-3", true},
		// The backend matches depot- labels case-sensitively and replaces the
		// rest with depot-ubuntu-latest, silently dropping the requested size.
		{"depotUppercase", "DEPOT-UBUNTU-24.04-32", true},
		{"depotLeadingWhitespace", "'  depot-ubuntu-latest'", true},
		// Within one element, only the first comma-separated part selects a
		// runner; the rest are secondary labels the backend ignores.
		{"commaSecondary", "depot-ubuntu-24.04 , dagger=0.18.6", false},
		{"commaSecondaryDepotLabel", "depot-ubuntu-latest,depot-windows-latest", false},
		{"commaUnsupportedPrimary", "depot-windows-latest,depot-ubuntu-latest", true},
		{"arrayUnknownDepotSecondary", "[depot-ubuntu-latest, depot-made-up]", false},
		{"arrayDistinctPrimaries", "[depot-ubuntu-latest, depot-windows-latest]", true},
		{"arrayDistinctSupportedPrimaries", "[depot-ubuntu-latest, depot-ubuntu-24.04-8]", true},
		{"arrayMappedDistinctPrimaries", "[ubuntu-latest, depot-ubuntu-24.04-8]", true},
		{"arrayIdenticalPrimaries", "[depot-ubuntu-latest, depot-ubuntu-latest]", false},
		{"arrayMappedIdenticalPrimaries", "[ubuntu-latest, depot-ubuntu-latest]", false},
		// The backend allows one element with secondaries per primary runner.
		{"arrayPlainThenQualified", "[depot-ubuntu-latest, 'depot-ubuntu-latest,dagger=1']", false},
		{"arrayQualifiedThenPlain", "['depot-ubuntu-latest,dagger=1', depot-ubuntu-latest]", false},
		{"arrayDoubleQualified", "['depot-ubuntu-latest,dagger=1', 'depot-ubuntu-latest,dagger=2']", true},
		{"arrayTrailingCommaQualified", "['depot-ubuntu-latest,', 'depot-ubuntu-latest, ']", true},
		{"arrayCustom", "[depot-ubuntu-latest, custom-runner]", true},
		{"expression", "${{ matrix.runner }}", false},
		{"expressionDepotPrefixed", "depot-${{ matrix.size }}", false},
		{"expressionCommaScalar", "${{ matrix.runner }},custom-runner", false},
		{"expressionWithPrimary", "['${{ matrix.runner }}', depot-ubuntu-latest]", false},
		{"expressionWithUnknownDepot", "['${{ matrix.runner }}', depot-made-up]", false},
		{"expressionWithCustom", "['${{ matrix.runner }}', custom-runner]", true},
		{"emptyArray", "[]", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := parseTestWorkflow(t, "jobs:\n  build:\n    runs-on: "+tc.runsOn+"\n")
			report := AnalyzeWorkflow(wf)
			want := 0
			if tc.wantCustom {
				want = 1
			}
			if len(report.Issues) != want {
				t.Fatalf("runs-on %s: got %+v, want %d issues", tc.runsOn, report.Issues, want)
			}
			if tc.wantCustom && (report.Issues[0].Feature != "runs-on (custom labels)" || report.Issues[0].Level != Partial) {
				t.Fatalf("unexpected custom runner issue: %+v", report.Issues[0])
			}
		})
	}
}

func TestAnalyzeWorkflowMatrixSelfHostedFromArray(t *testing.T) {
	wf := parseTestWorkflow(t, "jobs:\n  build:\n    runs-on: [self-hosted, linux]\n    strategy:\n      matrix:\n        go: [1, 2]\n")
	report := AnalyzeWorkflow(wf)
	if !HasCriticalIssues(report) {
		t.Fatalf("expected matrix + self-hosted issue, got %+v", report.Issues)
	}
}

func parseTestWorkflow(t *testing.T, jobs string) *migrate.WorkflowFile {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ci.yml")
	if err := os.WriteFile(path, []byte("on: push\n"+jobs), 0o644); err != nil {
		t.Fatal(err)
	}
	wf, err := migrate.ParseWorkflowFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return wf
}
