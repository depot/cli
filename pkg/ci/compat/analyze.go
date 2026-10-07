package compat

import (
	"fmt"
	"strings"

	"github.com/depot/cli/pkg/ci/migrate"
)

func AnalyzeWorkflow(workflow *migrate.WorkflowFile) *CompatibilityReport {
	if workflow == nil {
		return &CompatibilityReport{}
	}

	issues := append([]CompatibilityIssue{}, AnalyzeTriggers(workflow.Triggers)...)
	issues = append(issues, AnalyzeJobs(workflow.Jobs)...)

	for i := range issues {
		issues[i].File = workflow.Path
	}

	return &CompatibilityReport{
		File:   workflow.Path,
		Issues: issues,
	}
}

func AnalyzeTriggers(triggers []string) []CompatibilityIssue {
	issues := make([]CompatibilityIssue, 0)

	for _, trigger := range triggers {
		rule, ok := TriggerRules[trigger]
		if !ok {
			// Depot CI supports all trigger events except an explicit unsupported subset.
			// Unknown triggers are treated as supported to avoid false positives.
			continue
		}

		if rule.Supported == Supported {
			continue
		}

		issue := CompatibilityIssue{
			Feature:    trigger,
			Level:      rule.Supported,
			Message:    rule.Note,
			Suggestion: rule.Suggestion,
		}

		if issue.Message == "" {
			issue.Message = fmt.Sprintf("Trigger %q has compatibility level %d.", trigger, rule.Supported)
		}

		issues = append(issues, issue)
	}

	return issues
}

func AnalyzeJobs(jobs []migrate.JobInfo) []CompatibilityIssue {
	return analyzeJobs(jobs, runnerPolicy())
}

// analyzeJobs takes the runner policy so tests can pin label support instead of
// depending on the backend's current decisions.
func analyzeJobs(jobs []migrate.JobInfo, policy map[string]bool) []CompatibilityIssue {
	issues := make([]CompatibilityIssue, 0)

	containerRule := JobFeatureRules["container"]
	servicesRule := JobFeatureRules["services"]
	matrixSelfHostedRule := JobFeatureRules["strategy.matrix + self-hosted"]
	reusableRule := JobFeatureRules["uses"]
	runsOnRule := JobFeatureRules["runs-on (custom labels)"]
	unavailableRunsOnRule := JobFeatureRules["runs-on (unavailable labels)"]

	for _, job := range jobs {
		jobLabel := job.Name
		if jobLabel == "" {
			jobLabel = "unnamed job"
		}

		if job.HasContainer && containerRule.Supported != Supported {
			issues = append(issues, CompatibilityIssue{
				Feature:    "container",
				Level:      containerRule.Supported,
				Message:    fmt.Sprintf("Job %q uses a container: %s", jobLabel, containerRule.Note),
				Suggestion: containerRule.Suggestion,
			})
		}

		if job.HasServices && servicesRule.Supported != Supported {
			issues = append(issues, CompatibilityIssue{
				Feature:    "services",
				Level:      servicesRule.Supported,
				Message:    fmt.Sprintf("Job %q uses services: %s", jobLabel, servicesRule.Note),
				Suggestion: servicesRule.Suggestion,
			})
		}

		if job.UsesReusable != "" && !isLocalReusableWorkflow(job.UsesReusable) {
			issues = append(issues, CompatibilityIssue{
				Feature:    "uses",
				Level:      reusableRule.Supported,
				Message:    fmt.Sprintf("Job %q references non-local reusable workflow %q.", jobLabel, job.UsesReusable),
				Suggestion: "Use reusable workflows from the same repository path, such as ./.github/workflows/build.yml.",
			})
		}

		runsOn := jobRunsOnLabels(job)

		if job.HasMatrix && hasSelfHostedRunsOn(runsOn) {
			issues = append(issues, CompatibilityIssue{
				Feature:    "strategy.matrix + self-hosted",
				Level:      matrixSelfHostedRule.Supported,
				Message:    fmt.Sprintf("Job %q combines matrix and self-hosted runs-on labels: %s", jobLabel, matrixSelfHostedRule.Note),
				Suggestion: matrixSelfHostedRule.Suggestion,
			})
		}

		if hasCustomRunsOn(runsOn, policy) {
			issues = append(issues, CompatibilityIssue{
				Feature:    "runs-on (custom labels)",
				Level:      runsOnRule.Supported,
				Message:    fmt.Sprintf("Job %q uses runs-on %q: %s", jobLabel, job.RunsOn, runsOnRule.Note),
				Suggestion: runsOnRule.Suggestion,
			})
		}
		for _, label := range runsOn {
			if migrate.ClassifyLabel(label) != migrate.LabelUnavailableGitHub {
				continue
			}
			issues = append(issues, CompatibilityIssue{
				Feature:    "runs-on (unavailable labels)",
				Level:      unavailableRunsOnRule.Supported,
				Message:    fmt.Sprintf("Job %q uses runs-on %q: %s", jobLabel, label, unavailableRunsOnRule.Note),
				Suggestion: unavailableRunsOnRule.Suggestion,
			})
		}
	}

	return issues
}

func SummarizeReport(report *CompatibilityReport) string {
	if report == nil || len(report.Issues) == 0 {
		return "No compatibility issues found"
	}

	unsupported := 0
	inProgress := 0
	partial := 0

	for _, issue := range report.Issues {
		switch issue.Level {
		case Unsupported:
			unsupported++
		case InProgress:
			inProgress++
		case Partial:
			partial++
		}
	}

	summary := fmt.Sprintf("%d issues found", len(report.Issues))
	details := make([]string, 0, 3)

	if unsupported > 0 {
		details = append(details, fmt.Sprintf("%d unsupported", unsupported))
	}
	if partial > 0 {
		details = append(details, fmt.Sprintf("%d partial", partial))
	}
	if inProgress > 0 {
		details = append(details, fmt.Sprintf("%d in progress", inProgress))
	}

	if len(details) == 0 {
		return summary
	}

	return fmt.Sprintf("%s (%s)", summary, strings.Join(details, ", "))
}

func HasCriticalIssues(report *CompatibilityReport) bool {
	if report == nil {
		return false
	}

	for _, issue := range report.Issues {
		if issue.Level == Unsupported {
			return true
		}
	}

	return false
}

func isLocalReusableWorkflow(uses string) bool {
	return strings.HasPrefix(strings.TrimSpace(uses), "./")
}

// jobRunsOnLabels reads a JobInfo without RunsOnLabels as one scalar runs-on.
func jobRunsOnLabels(job migrate.JobInfo) []string {
	if job.RunsOnLabels != nil {
		return job.RunsOnLabels
	}
	return []string{job.RunsOn}
}

// hasCustomRunsOn reports whether Depot CI may not run a job on the runner its
// runs-on labels request, after migration rewrites standard GitHub labels.
//
// It mirrors the backend's label selection: each element that starts with
// depot- (case-sensitive) selects its first comma-separated part as the
// primary runner when that part is a known label. Remaining parts and unknown
// depot- elements are secondary labels; the backend ignores them, so they only
// matter when no element selects a runner. An element whose first part
// contains an expression selects its runner at run time, so a job using one is
// never reported for lacking a runner.
func hasCustomRunsOn(labels []string, policy map[string]bool) bool {
	// primaries records, per selected runner, whether an element carrying
	// secondary labels already selected it.
	primaries := make(map[string]bool)
	hasSecondary, hasExpression := false, false
	for _, label := range labels {
		if strings.TrimSpace(label) == "" {
			continue
		}

		// a literal comma before an expression fixes the primary runner and
		// guarantees secondaries, regardless of what the suffix expands to.
		if head, _, _ := strings.Cut(label, ","); strings.Contains(head, "${{") {
			hasExpression = true
			continue
		}

		// MapLabel leaves expression-containing elements untouched, so a
		// standard GitHub primary with a dynamic secondary stays unmapped.
		switch migrate.ClassifyLabel(label) {
		case migrate.LabelUnavailableGitHub:
			continue
		case migrate.LabelNonstandard:
			return true
		case migrate.LabelStandardGitHub:
			label, _, _ = migrate.MapLabel(label)
		}

		// ClassifyLabel ignores case and surrounding whitespace, but the backend
		// does not: "DEPOT-UBUNTU-24.04-32" silently runs on depot-ubuntu-latest.
		if !strings.HasPrefix(label, "depot-") {
			return true
		}

		parts := strings.Split(label, ",")
		primary := strings.TrimSpace(parts[0])
		supported, known := policy[primary]
		if !known {
			hasSecondary = true
			continue
		}
		if !supported {
			return true
		}
		// The backend rejects a runner selected twice with secondary labels. Like
		// the backend, a trailing comma counts as having secondaries.
		qualified := len(parts) > 1
		if qualified && primaries[primary] {
			return true
		}
		primaries[primary] = primaries[primary] || qualified
	}

	// The backend rejects jobs that select more than one distinct runner, or none.
	return len(primaries) > 1 || (len(primaries) == 0 && hasSecondary && !hasExpression)
}

func hasSelfHostedRunsOn(labels []string) bool {
	for _, label := range labels {
		for _, part := range strings.Split(label, ",") {
			if strings.ToLower(strings.TrimSpace(part)) == "self-hosted" {
				return true
			}
		}
	}
	return false
}
