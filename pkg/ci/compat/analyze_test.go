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

// testRunnerPolicy pins label support so these cases exercise label selection,
// not the backend's current decisions. MapLabel targets are listed so standard
// GitHub labels resolve through the policy like Depot labels.
var testRunnerPolicy = map[string]bool{
	"depot-ok":            true,
	"depot-ok-2":          true,
	"depot-no":            false,
	"depot-ubuntu-latest": true,
	"depot-ubuntu-22.04":  true,
	"depot-ubuntu-24.04":  false,
}

func TestAnalyzeJobsRunnerLabels(t *testing.T) {
	cases := []struct {
		name       string
		runsOn     string // YAML value of the job's runs-on
		wantCustom bool
	}{
		{"githubLatest", "ubuntu-latest", false},
		{"githubCaseWhitespace", "'  Ubuntu-22.04 '", false},
		{"githubMappedUnsupported", "ubuntu-24.04", true},
		{"depotSupported", "depot-ok", false},
		{"depotUnsupported", "depot-no", true},
		{"githubWindows", "windows-latest", true},
		{"unknown", "custom-runner", true},
		{"legacyUnderscore", "depot_ubuntu_latest", true},
		{"depotUnknownAlone", "depot-made-up", true},
		// The backend matches depot- labels case-sensitively and replaces the
		// rest with depot-ubuntu-latest, silently dropping the requested runner.
		{"depotUppercase", "DEPOT-OK", true},
		{"depotLeadingWhitespace", "'  depot-ok'", true},
		// Within one element, only the first comma-separated part selects a
		// runner; the rest are secondary labels the backend ignores.
		{"commaSecondary", "depot-ok , dagger=0.18.6", false},
		{"commaSecondaryDepotLabel", "depot-ok,depot-no", false},
		{"commaUnsupportedPrimary", "depot-no,depot-ok", true},
		{"arrayUnknownDepotSecondary", "[depot-ok, depot-made-up]", false},
		{"arrayDistinctPrimaries", "[depot-ok, depot-no]", true},
		{"arrayDistinctSupportedPrimaries", "[depot-ok, depot-ok-2]", true},
		{"arrayMappedDistinctPrimaries", "[ubuntu-latest, depot-ok]", true},
		{"arrayIdenticalPrimaries", "[depot-ok, depot-ok]", false},
		{"arrayMappedIdenticalPrimaries", "[ubuntu-latest, depot-ubuntu-latest]", false},
		// The backend allows one element with secondaries per primary runner.
		{"arrayPlainThenQualified", "[depot-ok, 'depot-ok,dagger=1']", false},
		{"arrayQualifiedThenPlain", "['depot-ok,dagger=1', depot-ok]", false},
		{"arrayDoubleQualified", "['depot-ok,dagger=1', 'depot-ok,dagger=2']", true},
		{"arrayTrailingCommaQualified", "['depot-ok,', 'depot-ok, ']", true},
		{"arrayCustom", "[depot-ok, custom-runner]", true},
		{"expression", "${{ matrix.runner }}", false},
		{"expressionDepotPrefixed", "depot-${{ matrix.size }}", false},
		{"expressionCommaScalar", "${{ matrix.runner }},custom-runner", false},
		{"expressionWithPrimary", "['${{ matrix.runner }}', depot-ok]", false},
		{"expressionWithUnknownDepot", "['${{ matrix.runner }}', depot-made-up]", false},
		{"expressionWithCustom", "['${{ matrix.runner }}', custom-runner]", true},
		// Commas inside an expression in the first part still leave the
		// primary runner dynamic.
		{"expressionFormatComma", `"${{ format('{0},{1}', matrix.a, matrix.b) }}"`, false},
		{"expressionJoinComma", `"depot-${{ join(matrix.x, ',') }}"`, false},
		// An expression after the first literal comma only affects secondary
		// labels, so the literal primary is still checked.
		{"dynamicSecondarySupported", "'depot-ok,dagger=${{ matrix.version }}'", false},
		{"dynamicSecondaryUnsupported", "'depot-no,dagger=${{ matrix.version }}'", true},
		{"dynamicSecondaryUnknownDepot", "'depot-made-up,x=${{ matrix.v }}'", true},
		{"dynamicSecondaryUnknownDepotWithPrimary", "['depot-made-up,x=${{ matrix.v }}', depot-ok]", false},
		{"dynamicSecondaryGitHub", "'ubuntu-latest,x=${{ matrix.v }}'", true},
		{"dynamicSecondaryUppercase", "'DEPOT-OK,x=${{ matrix.v }}'", true},
		{"dynamicSecondaryThenQualified", "['depot-ok,x=${{ matrix.v }}', 'depot-ok,dagger=2']", true},
		{"dynamicSecondaryThenPlain", "['depot-ok,x=${{ matrix.v }}', depot-ok]", false},
		{"dynamicSecondaryDistinctPrimaries", "['depot-ok,x=${{ matrix.v }}', depot-ok-2]", true},
		{"emptyArray", "[]", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := parseTestWorkflow(t, "jobs:\n  build:\n    runs-on: "+tc.runsOn+"\n")
			assertCustomRunsOn(t, analyzeJobs(wf.Jobs, testRunnerPolicy), tc.wantCustom)
		})
	}
}

func TestAnalyzeJobsRunnerLabelFollowsPolicy(t *testing.T) {
	wf := parseTestWorkflow(t, "jobs:\n  build:\n    runs-on: depot-x\n")
	for _, tc := range []struct {
		name       string
		policy     map[string]bool
		wantCustom bool
	}{
		{"supported", map[string]bool{"depot-x": true}, false},
		{"unsupported", map[string]bool{"depot-x": false}, true},
		{"unknown", map[string]bool{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertCustomRunsOn(t, analyzeJobs(wf.Jobs, tc.policy), tc.wantCustom)
		})
	}
}

// TestAnalyzeWorkflowEmbeddedRunnerPolicy checks that the public entry point
// reads the embedded policy, using whichever labels it currently lists.
func TestAnalyzeWorkflowEmbeddedRunnerPolicy(t *testing.T) {
	tested := map[bool]bool{}
	for label, supported := range runnerPolicy() {
		if tested[supported] {
			continue
		}
		tested[supported] = true
		wf := parseTestWorkflow(t, "jobs:\n  build:\n    runs-on: "+label+"\n")
		assertCustomRunsOn(t, AnalyzeWorkflow(wf).Issues, !supported)
	}
}

func assertCustomRunsOn(t *testing.T, issues []CompatibilityIssue, wantCustom bool) {
	t.Helper()
	want := 0
	if wantCustom {
		want = 1
	}
	if len(issues) != want {
		t.Fatalf("got %+v, want %d issues", issues, want)
	}
	if wantCustom && (issues[0].Feature != "runs-on (custom labels)" || issues[0].Level != Partial) {
		t.Fatalf("unexpected custom runner issue: %+v", issues[0])
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
