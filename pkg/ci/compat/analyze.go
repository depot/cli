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

		if job.HasMatrix && hasSelfHostedRunsOn(job.RunsOn) {
			issues = append(issues, CompatibilityIssue{
				Feature:    "strategy.matrix + self-hosted",
				Level:      matrixSelfHostedRule.Supported,
				Message:    fmt.Sprintf("Job %q combines matrix and self-hosted runs-on labels: %s", jobLabel, matrixSelfHostedRule.Note),
				Suggestion: matrixSelfHostedRule.Suggestion,
			})
		}

		if hasCustomRunsOn(job.RunsOn) {
			issues = append(issues, CompatibilityIssue{
				Feature:    "runs-on (custom labels)",
				Level:      runsOnRule.Supported,
				Message:    fmt.Sprintf("Job %q uses runs-on %q: %s", jobLabel, job.RunsOn, runsOnRule.Note),
				Suggestion: runsOnRule.Suggestion,
			})
		}

		for _, label := range parseRunsOnLabels(job.RunsOn) {
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

// unsupportedDepotRunnerPrefixes excludes depot-* families that
// migrate.ClassifyLabel treats as Depot-native but Depot CI cannot run:
// sandboxes are Linux-only (https://depot.dev/docs/ci/overview#depot-ci-sandboxes).
// Prefixes cover version and size suffixes such as depot-windows-2022-8.
var unsupportedDepotRunnerPrefixes = []string{"depot-macos", "depot-windows"}

func hasCustomRunsOn(runsOn string) bool {
	labels := parseRunsOnLabels(runsOn)
	for _, label := range labels {
		switch migrate.ClassifyLabel(label) {
		case migrate.LabelDepotNative:
			if isUnsupportedDepotRunner(label) {
				return true
			}
			continue
		case migrate.LabelStandardGitHub, migrate.LabelExpression, migrate.LabelUnavailableGitHub:
			continue
		}
		return true
	}

	return false
}

// isUnsupportedDepotRunner expects a label normalized by parseRunsOnLabels.
func isUnsupportedDepotRunner(label string) bool {
	for _, prefix := range unsupportedDepotRunnerPrefixes {
		if strings.HasPrefix(label, prefix) {
			return true
		}
	}
	return false
}

func hasSelfHostedRunsOn(runsOn string) bool {
	labels := parseRunsOnLabels(runsOn)
	for _, label := range labels {
		if label == "self-hosted" {
			return true
		}
	}
	return false
}

func parseRunsOnLabels(runsOn string) []string {
	trimmed := strings.TrimSpace(runsOn)
	if trimmed == "" {
		return nil
	}

	parts := strings.Split(trimmed, ",")
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		label := strings.ToLower(strings.TrimSpace(part))
		if label != "" {
			labels = append(labels, label)
		}
	}

	return labels
}
