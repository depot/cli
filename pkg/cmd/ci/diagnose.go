package ci

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

var ciDiagnose = api.CIGetFailureDiagnosis

const (
	diagnoseTextWidth     = 96
	maxGroupEvidenceLines = 5
)

func NewCmdDiagnose() *cobra.Command {
	var (
		orgID      string
		token      string
		output     string
		runID      string
		workflowID string
		jobID      string
		attemptID  string
	)

	cmd := &cobra.Command{
		Use:   "diagnose --run <run-id> | --workflow <workflow-id> | --job <job-id> | --attempt <attempt-id>",
		Short: "Diagnose a failed CI run, workflow, job, or attempt",
		Long:  "Diagnose a failed CI run, workflow, job, or attempt using bounded stored failure context.",
		Example: `  depot ci diagnose --run <run-id>
  depot ci diagnose --workflow <workflow-id>
  depot ci diagnose --job <job-id> --output json
  depot ci diagnose --attempt <attempt-id>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTextOrJSONOutput(output); err != nil {
				return err
			}
			if len(args) > 0 {
				return fmt.Errorf("positional target IDs are not supported; use exactly one of --run, --workflow, --job, or --attempt")
			}
			targetID, diagnosisTargetType, err := diagnosisTargetFromSelectors(runID, workflowID, jobID, attemptID)
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			resp, err := ciDiagnose(ctx, tokenVal, orgID, &civ1.GetFailureDiagnosisRequest{
				TargetId:   targetID,
				TargetType: diagnosisTargetType,
			})
			if err != nil {
				return fmt.Errorf("failed to diagnose CI target: %w", err)
			}

			commandOrgID := ""
			if cmd.Flags().Changed("org") {
				commandOrgID = orgID
			}

			if outputIsJSON(output) {
				return writeJSON(buildDiagnoseJSON(resp, commandOrgID))
			}
			printDiagnoseResponse(cmd.OutOrStdout(), resp, commandOrgID)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (text, json)")
	cmd.Flags().StringVar(&runID, "run", "", "Run ID to diagnose")
	cmd.Flags().StringVar(&workflowID, "workflow", "", "Workflow ID to diagnose")
	cmd.Flags().StringVar(&jobID, "job", "", "Job ID to diagnose")
	cmd.Flags().StringVar(&attemptID, "attempt", "", "Job attempt ID to diagnose")

	return cmd
}

func diagnosisTargetFromSelectors(runID, workflowID, jobID, attemptID string) (string, civ1.FailureDiagnosisTargetType, error) {
	if countNonEmpty(runID, workflowID, jobID, attemptID) != 1 {
		return "", civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_UNSPECIFIED, fmt.Errorf("expected exactly one target selector: --run, --workflow, --job, or --attempt")
	}
	switch {
	case runID != "":
		return runID, civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_RUN, nil
	case workflowID != "":
		return workflowID, civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_WORKFLOW, nil
	case jobID != "":
		return jobID, civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_JOB, nil
	default:
		return attemptID, civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_ATTEMPT, nil
	}
}

func buildDiagnoseCommandJSON(cmd *civ1.DrillDownCommand, orgID string) diagnoseCommandJSON {
	argv := append([]string(nil), cmd.GetArgv()...)
	if orgID != "" && len(argv) > 0 {
		argv = append(argv, "--org", orgID)
	}
	return diagnoseCommandJSON{
		Kind:              diagnosisCommandKindString(cmd.GetKind()),
		Available:         cmd.GetAvailable(),
		UnavailableReason: cmd.GetUnavailableReason(),
		TargetID:          cmd.GetTargetId(),
		Label:             cmd.GetLabel(),
		Argv:              argv,
		Shell:             shellJoin(argv),
	}
}

func buildDiagnoseCommandJSONs(commands []*civ1.DrillDownCommand, capabilities *civ1.FailureDiagnosisCommandCapabilities, orgID string, textOnly bool) []diagnoseCommandJSON {
	out := make([]diagnoseCommandJSON, 0, len(commands))
	for _, command := range commands {
		if command == nil {
			continue
		}
		if command.GetKind() == civ1.DrillDownCommandKind_DRILL_DOWN_COMMAND_KIND_SUMMARY && !capabilities.GetSummaryCommandAvailable() {
			continue
		}
		if textOnly && (!command.GetAvailable() || len(command.GetArgv()) == 0) {
			continue
		}
		out = append(out, buildDiagnoseCommandJSON(command, orgID))
	}
	return out
}

func printDiagnoseResponse(w io.Writer, resp *civ1.GetFailureDiagnosisResponse, commandOrgID string) {
	fmt.Fprintf(w, "Org: %s\n", resp.GetOrgId())
	if target := resp.GetTarget(); target != nil {
		fmt.Fprintf(w, "Target: %s %s", diagnosisTargetTypeString(target.GetTargetType()), target.GetTargetId())
		if status := diagnosisResourceStatusDisplayString(target.GetStatus()); status != "" {
			fmt.Fprintf(w, " (%s)", status)
		}
		fmt.Fprintln(w)
	}
	printDiagnosisContext(w, resp.GetContext())

	switch resp.GetState() {
	case civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_EMPTY:
		printEmptyDiagnosis(w, resp)
	case civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_OVER_LIMIT:
		printOverLimitDiagnosis(w, resp, commandOrgID)
	case civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_FOCUSED_FAILURE:
		printFocusedDiagnosis(w, resp, commandOrgID)
	default:
		printGroupedDiagnosis(w, resp, commandOrgID)
	}
}

func printDiagnosisContext(w io.Writer, context *civ1.FailureDiagnosisContext) {
	if context.GetRunId() != "" {
		line := fmt.Sprintf("Run: %s", context.GetRunId())
		if status := diagnosisResourceStatusDisplayString(context.GetRunStatus()); status != "" {
			line += fmt.Sprintf(" (%s)", status)
		}
		fmt.Fprintln(w, line)
	}
	if context.GetRepo() != "" || context.GetRef() != "" || context.GetSha() != "" {
		fmt.Fprintf(w, "Source: %s", context.GetRepo())
		if context.GetRef() != "" {
			fmt.Fprintf(w, " @ %s", context.GetRef())
		}
		if context.GetSha() != "" && context.GetSha() != context.GetRef() {
			fmt.Fprintf(w, " (%s)", context.GetSha())
		}
		fmt.Fprintln(w)
	}
	if context.GetWorkflowId() != "" {
		fmt.Fprintf(w, "Workflow: %s", firstNonEmpty(context.GetWorkflowName(), context.GetWorkflowPath(), context.GetWorkflowId()))
		if context.GetWorkflowPath() != "" && context.GetWorkflowPath() != context.GetWorkflowName() {
			fmt.Fprintf(w, " [%s]", context.GetWorkflowPath())
		}
		if status := diagnosisResourceStatusDisplayString(context.GetWorkflowStatus()); status != "" {
			fmt.Fprintf(w, " (%s)", status)
		}
		fmt.Fprintln(w)
	}
	if context.GetJobId() != "" {
		fmt.Fprintf(w, "Job: %s", firstNonEmpty(context.GetJobDisplayName(), context.GetJobKey(), context.GetJobId()))
		if status := diagnosisResourceStatusDisplayString(context.GetJobStatus()); status != "" {
			fmt.Fprintf(w, " (%s)", status)
		}
		if conclusion := diagnosisConclusionDisplayString(context.GetJobConclusion()); conclusion != "" {
			fmt.Fprintf(w, " conclusion=%s", conclusion)
		}
		fmt.Fprintln(w)
	}
	if context.GetAttemptId() != "" {
		fmt.Fprintf(w, "Attempt: #%d %s", context.GetAttempt(), context.GetAttemptId())
		if status := diagnosisResourceStatusDisplayString(context.GetAttemptStatus()); status != "" {
			fmt.Fprintf(w, " (%s)", status)
		}
		if conclusion := diagnosisConclusionDisplayString(context.GetAttemptConclusion()); conclusion != "" {
			fmt.Fprintf(w, " conclusion=%s", conclusion)
		}
		fmt.Fprintln(w)
	}
	if len(context.GetTruncatedContextFields()) > 0 {
		fmt.Fprintf(w, "Context fields truncated: %s\n", strings.Join(context.GetTruncatedContextFields(), ", "))
	}
}

func printEmptyDiagnosis(w io.Writer, resp *civ1.GetFailureDiagnosisResponse) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "No CI failures found for this target.")
	if resp.GetEmptyReason() != "" {
		fmt.Fprintf(w, "Reason: %s\n", resp.GetEmptyReason())
	}
	printBoundsSummary(w, resp)
}

func printOverLimitDiagnosis(w io.Writer, resp *civ1.GetFailureDiagnosisResponse, commandOrgID string) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Diagnosis is over limit.")
	bounds := resp.GetBounds()
	if bounds != nil {
		fmt.Fprintf(w, "Failed/problem candidates: %d", bounds.GetFailedProblemCandidateCount())
		if bounds.GetFailedProblemCandidateCap() > 0 {
			fmt.Fprintf(w, " (cap %d)", bounds.GetFailedProblemCandidateCap())
		}
		fmt.Fprintln(w)
		if bounds.GetSkippedDependentCount() > 0 {
			fmt.Fprintf(w, "Skipped dependents: %d\n", bounds.GetSkippedDependentCount())
		}
	}
	if len(resp.GetOverLimitBreakdown()) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Narrower targets:")
		for _, row := range resp.GetOverLimitBreakdown() {
			fmt.Fprintf(w, "  %s %s", diagnosisTargetTypeString(row.GetTargetType()), row.GetTargetId())
			if row.GetLabel() != "" {
				fmt.Fprintf(w, " [%s]", row.GetLabel())
			}
			if status := diagnosisResourceStatusDisplayString(row.GetStatus()); status != "" {
				fmt.Fprintf(w, " (%s)", status)
			}
			fmt.Fprintf(w, ": %d failed/problem candidates\n", row.GetFailedProblemCandidateCount())
			for _, command := range buildDiagnoseCommandJSONs(row.GetNextCommands(), resp.GetCommandCapabilities(), commandOrgID, true) {
				fmt.Fprintf(w, "    %s: %s\n", firstNonEmpty(command.Label, "Command"), command.Shell)
			}
		}
	}
	printBoundsSummary(w, resp)
}

func printFocusedDiagnosis(w io.Writer, resp *civ1.GetFailureDiagnosisResponse, commandOrgID string) {
	fmt.Fprintln(w)
	if len(resp.GetRepresentativeAttempts()) == 0 {
		fmt.Fprintln(w, "Focused diagnosis returned no representative attempts.")
		printNextCommands(w, buildDiagnoseCommandJSONs(resp.GetNextCommands(), resp.GetCommandCapabilities(), commandOrgID, true), "Next commands")
		printBoundsSummary(w, resp)
		return
	}

	fmt.Fprintln(w, "Focused diagnosis:")
	for _, representative := range resp.GetRepresentativeAttempts() {
		printRepresentativeAttempt(w, resp.GetOrgId(), representative, resp.GetCommandCapabilities(), commandOrgID, "  ")
	}
	printSummaryUnavailableNote(w, resp.GetCommandCapabilities())
	printBoundsSummary(w, resp)
}

func printGroupedDiagnosis(w io.Writer, resp *civ1.GetFailureDiagnosisResponse, commandOrgID string) {
	fmt.Fprintln(w)
	bounds := resp.GetBounds()
	if bounds != nil && bounds.GetTotalFailureGroupCount() > 0 {
		fmt.Fprintf(w, "Failure groups: %d", bounds.GetTotalFailureGroupCount())
		if bounds.GetOmittedFailureGroupCount() > 0 {
			fmt.Fprintf(w, " (%d omitted)", bounds.GetOmittedFailureGroupCount())
		}
		fmt.Fprintln(w)
	} else {
		fmt.Fprintf(w, "Failure groups: %d\n", len(resp.GetFailureGroups()))
	}
	for i, group := range resp.GetFailureGroups() {
		fmt.Fprintf(w, "\nGroup %d: %s\n", i+1, firstNonEmpty(group.GetErrorMessage(), "failure group"))
		fmt.Fprintf(w, "  %d %s\n", group.GetCount(), pluralize("failure", int(group.GetCount())))
		if group.GetErrorMessageTruncated() {
			fmt.Fprintf(w, "  Error truncated%s\n", truncatedSuffix(true, group.GetErrorMessageOriginalLength()))
		}
		representativeError := commonRepresentativeError(group)
		showRepresentativeErrors := true
		if representativeError != "" && representativeError != group.GetErrorMessage() {
			fmt.Fprintf(w, "  Where: %s\n", representativeError)
			showRepresentativeErrors = false
		} else if representativeError == group.GetErrorMessage() {
			showRepresentativeErrors = false
		}
		if group.GetDiagnosis() != "" {
			fmt.Fprintln(w)
			printWrappedSection(w, "Diagnosis", group.GetDiagnosis(), "  ")
		}
		if group.GetPossibleFix() != "" {
			fmt.Fprintln(w)
			printWrappedSection(w, "Possible fix", group.GetPossibleFix(), "  ")
		}
		if len(group.GetRepresentatives()) > 0 {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "  Attempts:")
			for _, representative := range group.GetRepresentatives() {
				printCompactRepresentativeAttempt(w, resp.GetOrgId(), representative, resp.GetCommandCapabilities(), commandOrgID, "    ", showRepresentativeErrors)
			}
		}
		if group.GetOmittedRepresentativeCount() > 0 {
			fmt.Fprintf(
				w,
				"  Showing %d of %d similar attempts for this group.\n",
				len(group.GetRepresentatives()),
				len(group.GetRepresentatives())+int(group.GetOmittedRepresentativeCount()),
			)
		}
		printGroupEvidence(w, group, "  ")
	}
	printSummaryUnavailableNote(w, resp.GetCommandCapabilities())
	printBoundsSummary(w, resp)
}

func printCompactRepresentativeAttempt(w io.Writer, orgID string, representative *civ1.RepresentativeAttempt, capabilities *civ1.FailureDiagnosisCommandCapabilities, commandOrgID string, indent string, showError bool) {
	fmt.Fprintf(w, "%s- #%d %s", indent, representative.GetAttempt(), representative.GetAttemptId())
	if representative.GetJobKey() != "" || representative.GetJobDisplayName() != "" {
		fmt.Fprintf(w, "  %s", firstNonEmpty(representative.GetJobDisplayName(), representative.GetJobKey()))
	}
	if status := diagnosisResourceStatusDisplayString(representative.GetAttemptStatus()); status != "" {
		fmt.Fprintf(w, " (%s)", status)
	}
	fmt.Fprintln(w)
	if showError && representative.GetErrorMessage() != "" {
		fmt.Fprintf(w, "%s  Error: %s%s\n", indent, representative.GetErrorMessage(), truncatedSuffix(representative.GetErrorMessageTruncated(), representative.GetErrorMessageOriginalLength()))
	}
	for _, command := range buildDiagnoseCommandJSONs(representative.GetNextCommands(), capabilities, commandOrgID, true) {
		fmt.Fprintf(w, "%s  %s: %s\n", indent, firstNonEmpty(command.Label, "Command"), command.Shell)
	}
	if orgID != "" && representative.GetWorkflowId() != "" && representative.GetJobId() != "" && representative.GetAttemptId() != "" {
		fmt.Fprintf(w, "%s  View: %s\n", indent, statusAttemptViewURL(orgID, representative.GetWorkflowId(), representative.GetJobId(), representative.GetAttemptId()))
	}
}

func commonRepresentativeError(group *civ1.FailureGroup) string {
	var common string
	for _, representative := range group.GetRepresentatives() {
		if representative.GetErrorMessage() == "" {
			continue
		}
		if common == "" {
			common = representative.GetErrorMessage()
			continue
		}
		if representative.GetErrorMessage() != common {
			return ""
		}
	}
	return common
}

func printRepresentativeAttempt(w io.Writer, orgID string, representative *civ1.RepresentativeAttempt, capabilities *civ1.FailureDiagnosisCommandCapabilities, commandOrgID string, indent string) {
	fmt.Fprintf(w, "%sAttempt #%d %s", indent, representative.GetAttempt(), representative.GetAttemptId())
	if representative.GetJobKey() != "" || representative.GetJobDisplayName() != "" {
		fmt.Fprintf(w, " for %s", firstNonEmpty(representative.GetJobDisplayName(), representative.GetJobKey()))
	}
	if status := diagnosisResourceStatusDisplayString(representative.GetAttemptStatus()); status != "" {
		fmt.Fprintf(w, " (%s)", status)
	}
	fmt.Fprintln(w)
	if representative.GetErrorMessage() != "" {
		fmt.Fprintf(w, "%s  Error: %s%s\n", indent, representative.GetErrorMessage(), truncatedSuffix(representative.GetErrorMessageTruncated(), representative.GetErrorMessageOriginalLength()))
	}
	if representative.GetDiagnosis() != "" {
		fmt.Fprintf(w, "%s  Diagnosis: %s\n", indent, representative.GetDiagnosis())
	}
	if representative.GetPossibleFix() != "" {
		fmt.Fprintf(w, "%s  Possible fix: %s\n", indent, representative.GetPossibleFix())
	}
	if len(representative.GetRelevantLines()) > 0 {
		fmt.Fprintf(w, "%s  Relevant lines:\n", indent)
		for _, line := range representative.GetRelevantLines() {
			prefix := fmt.Sprintf("%d", line.GetLineNumber())
			if line.GetStepId() != "" {
				prefix = line.GetStepId() + ":" + prefix
			}
			fmt.Fprintf(w, "%s    %s: %s%s\n", indent, prefix, line.GetContent(), truncatedSuffix(line.GetContentTruncated(), line.GetContentOriginalLength()))
		}
	}
	for _, command := range buildDiagnoseCommandJSONs(representative.GetNextCommands(), capabilities, commandOrgID, true) {
		fmt.Fprintf(w, "%s  %s: %s\n", indent, firstNonEmpty(command.Label, "Command"), command.Shell)
	}
	if orgID != "" && representative.GetWorkflowId() != "" && representative.GetJobId() != "" && representative.GetAttemptId() != "" {
		fmt.Fprintf(w, "%s  View: %s\n", indent, statusAttemptViewURL(orgID, representative.GetWorkflowId(), representative.GetJobId(), representative.GetAttemptId()))
	}
}

func printGroupEvidence(w io.Writer, group *civ1.FailureGroup, indent string) {
	type evidenceLine struct {
		prefix         string
		content        string
		truncated      bool
		originalLength uint32
	}

	seen := map[string]struct{}{}
	lines := make([]evidenceLine, 0, maxGroupEvidenceLines)
	for _, representative := range group.GetRepresentatives() {
		for _, line := range representative.GetRelevantLines() {
			if isHumanOutputWrapperEvidenceLine(line.GetContent()) {
				continue
			}
			prefix := fmt.Sprintf("%d", line.GetLineNumber())
			if line.GetStepId() != "" {
				prefix = line.GetStepId() + ":" + prefix
			}
			key := normalizeEvidenceContent(line.GetContent())
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			lines = append(lines, evidenceLine{
				prefix:         prefix,
				content:        line.GetContent(),
				truncated:      line.GetContentTruncated(),
				originalLength: line.GetContentOriginalLength(),
			})
			if len(lines) == maxGroupEvidenceLines {
				break
			}
		}
		if len(lines) == maxGroupEvidenceLines {
			break
		}
	}
	if len(lines) == 0 {
		return
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%sEvidence:\n", indent)
	for _, line := range lines {
		fmt.Fprintf(w, "%s  - %s: %s%s\n", indent, line.prefix, line.content, truncatedSuffix(line.truncated, line.originalLength))
	}
}

func normalizeEvidenceContent(content string) string {
	return strings.Join(strings.Fields(content), " ")
}

// Suppresses generic shell/package-manager wrapper lines from human group evidence only.
// The API payload, grouping, and JSON output still preserve the original relevant lines.
func isHumanOutputWrapperEvidenceLine(content string) bool {
	normalized := strings.ToLower(normalizeEvidenceContent(content))
	return strings.HasPrefix(normalized, "##[error]script exited with code ") ||
		strings.Contains(normalized, "err_pnpm_recursive_run_first_fail") ||
		strings.Contains(normalized, "elifecycle command failed")
}

func printNextCommands(w io.Writer, commands []diagnoseCommandJSON, title string) {
	if len(commands) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s:\n", title)
	for _, command := range commands {
		fmt.Fprintf(w, "  %s: %s\n", firstNonEmpty(command.Label, "Command"), command.Shell)
	}
}

func printSummaryUnavailableNote(w io.Writer, capabilities *civ1.FailureDiagnosisCommandCapabilities) {
	if capabilities == nil || capabilities.GetSummaryCommandAvailable() {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Summary drill-down commands are not available in this build.")
}

func printBoundsSummary(w io.Writer, resp *civ1.GetFailureDiagnosisResponse) {
	bounds := resp.GetBounds()
	if bounds == nil {
		return
	}
	if bounds.GetOmittedFailureGroupCount() > 0 {
		fmt.Fprintf(w, "Omitted failure groups: %d; run a narrower diagnose command for details.\n", bounds.GetOmittedFailureGroupCount())
	}
	if bounds.GetOmittedAttemptCount() > 0 {
		fmt.Fprintf(w, "Omitted attempts: %d.\n", bounds.GetOmittedAttemptCount())
	}
	if bounds.GetOmittedWorkflowBreakdownCount() > 0 {
		fmt.Fprintf(w, "Omitted workflow breakdown rows: %d.\n", bounds.GetOmittedWorkflowBreakdownCount())
	}
	if bounds.GetOmittedJobBreakdownCount() > 0 {
		fmt.Fprintf(w, "Omitted job breakdown rows: %d.\n", bounds.GetOmittedJobBreakdownCount())
	}
	if bounds.GetTruncated() {
		fmt.Fprintln(w, "Output was truncated by diagnosis bounds.")
	}
}

func truncatedSuffix(truncated bool, originalLength uint32) string {
	if !truncated {
		return ""
	}
	if originalLength == 0 {
		return " (truncated)"
	}
	return fmt.Sprintf(" (truncated from %d chars)", originalLength)
}

func printWrappedSection(w io.Writer, title, text, indent string) {
	fmt.Fprintf(w, "%s%s:\n", indent, title)
	printWrappedText(w, text, indent+"  ", diagnoseTextWidth)
}

func printWrappedText(w io.Writer, text, indent string, width int) {
	available := width - len(indent)
	if available < 20 {
		available = 20
	}
	for _, paragraph := range strings.Split(text, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			fmt.Fprintln(w, indent)
			continue
		}

		line := ""
		for _, word := range words {
			if line == "" {
				line = word
				continue
			}
			if len(line)+1+len(word) > available {
				fmt.Fprintln(w, indent+line)
				line = word
				continue
			}
			line += " " + word
		}
		if line != "" {
			fmt.Fprintln(w, indent+line)
		}
	}
}

func pluralize(singular string, count int) string {
	if count == 1 {
		return singular
	}
	return singular + "s"
}

type diagnoseJSONDocument struct {
	OrgID                  string                           `json:"org_id"`
	State                  string                           `json:"state"`
	EmptyReason            string                           `json:"empty_reason,omitempty"`
	Target                 diagnoseTargetJSON               `json:"target"`
	Context                diagnoseContextJSON              `json:"context"`
	CommandCapabilities    diagnoseCommandCapabilitiesJSON  `json:"command_capabilities"`
	Bounds                 diagnoseBoundsJSON               `json:"bounds"`
	FailureGroups          []diagnoseFailureGroupJSON       `json:"failure_groups"`
	RepresentativeAttempts []diagnoseRepresentativeJSON     `json:"representative_attempts"`
	NextCommands           []diagnoseCommandJSON            `json:"next_commands"`
	OverLimitBreakdown     []diagnoseOverLimitBreakdownJSON `json:"over_limit_breakdown"`
}

type diagnoseTargetJSON struct {
	TargetID   string `json:"target_id"`
	TargetType string `json:"target_type"`
	Status     string `json:"status"`
}

type diagnoseContextJSON struct {
	RunID                  string   `json:"run_id"`
	Repo                   string   `json:"repo"`
	Ref                    string   `json:"ref"`
	Sha                    string   `json:"sha"`
	HeadSha                string   `json:"head_sha"`
	Trigger                string   `json:"trigger"`
	RunStatus              string   `json:"run_status"`
	WorkflowID             string   `json:"workflow_id"`
	WorkflowName           string   `json:"workflow_name"`
	WorkflowPath           string   `json:"workflow_path"`
	WorkflowStatus         string   `json:"workflow_status"`
	JobID                  string   `json:"job_id"`
	JobKey                 string   `json:"job_key"`
	JobDisplayName         string   `json:"job_display_name"`
	JobStatus              string   `json:"job_status"`
	JobConclusion          string   `json:"job_conclusion"`
	AttemptID              string   `json:"attempt_id"`
	Attempt                int32    `json:"attempt"`
	AttemptStatus          string   `json:"attempt_status"`
	AttemptConclusion      string   `json:"attempt_conclusion"`
	TruncatedContextFields []string `json:"truncated_context_fields"`
}

type diagnoseCommandCapabilitiesJSON struct {
	SummaryCommandAvailable bool `json:"summary_command_available"`
}

type diagnoseBoundsJSON struct {
	FailedProblemCandidateCount     uint32 `json:"failed_problem_candidate_count"`
	FailedProblemCandidateCap       uint32 `json:"failed_problem_candidate_cap"`
	TotalProblemJobCount            uint32 `json:"total_problem_job_count"`
	SkippedDependentCount           uint32 `json:"skipped_dependent_count"`
	TotalFailureGroupCount          uint32 `json:"total_failure_group_count"`
	OmittedFailureGroupCount        uint32 `json:"omitted_failure_group_count"`
	FailureGroupLimit               uint32 `json:"failure_group_limit"`
	RepresentativesPerGroupLimit    uint32 `json:"representatives_per_group_limit"`
	RecentAttemptLimit              uint32 `json:"recent_attempt_limit"`
	TotalAttemptCount               uint32 `json:"total_attempt_count"`
	OmittedAttemptCount             uint32 `json:"omitted_attempt_count"`
	RelevantLineLimit               uint32 `json:"relevant_line_limit"`
	ErrorLineBodyCharLimit          uint32 `json:"error_line_body_char_limit"`
	ErrorMessageCharLimit           uint32 `json:"error_message_char_limit"`
	ContextLabelCharLimit           uint32 `json:"context_label_char_limit"`
	OverLimitWorkflowBreakdownLimit uint32 `json:"over_limit_workflow_breakdown_limit"`
	OverLimitJobBreakdownLimit      uint32 `json:"over_limit_job_breakdown_limit"`
	OmittedWorkflowBreakdownCount   uint32 `json:"omitted_workflow_breakdown_count"`
	OmittedJobBreakdownCount        uint32 `json:"omitted_job_breakdown_count"`
	Truncated                       bool   `json:"truncated"`
}

type diagnoseFailureGroupJSON struct {
	Fingerprint                string                       `json:"fingerprint"`
	Source                     string                       `json:"source"`
	Count                      uint32                       `json:"count"`
	ErrorMessage               string                       `json:"error_message"`
	ErrorMessageTruncated      bool                         `json:"error_message_truncated"`
	ErrorMessageOriginalLength uint32                       `json:"error_message_original_length"`
	Diagnosis                  string                       `json:"diagnosis"`
	PossibleFix                string                       `json:"possible_fix"`
	Representatives            []diagnoseRepresentativeJSON `json:"representatives"`
	OmittedRepresentativeCount uint32                       `json:"omitted_representative_count"`
}

type diagnoseRepresentativeJSON struct {
	RunID                      string                     `json:"run_id"`
	WorkflowID                 string                     `json:"workflow_id"`
	WorkflowName               string                     `json:"workflow_name"`
	WorkflowPath               string                     `json:"workflow_path"`
	JobID                      string                     `json:"job_id"`
	JobKey                     string                     `json:"job_key"`
	JobDisplayName             string                     `json:"job_display_name"`
	JobStatus                  string                     `json:"job_status"`
	JobConclusion              string                     `json:"job_conclusion"`
	AttemptID                  string                     `json:"attempt_id"`
	Attempt                    int32                      `json:"attempt"`
	AttemptStatus              string                     `json:"attempt_status"`
	AttemptConclusion          string                     `json:"attempt_conclusion"`
	ErrorMessage               string                     `json:"error_message"`
	ErrorMessageTruncated      bool                       `json:"error_message_truncated"`
	ErrorMessageOriginalLength uint32                     `json:"error_message_original_length"`
	Diagnosis                  string                     `json:"diagnosis"`
	PossibleFix                string                     `json:"possible_fix"`
	RelevantLines              []diagnoseRelevantLineJSON `json:"relevant_lines"`
	NextCommands               []diagnoseCommandJSON      `json:"next_commands"`
}

type diagnoseRelevantLineJSON struct {
	StepID                string `json:"step_id"`
	LineNumber            uint32 `json:"line_number"`
	Content               string `json:"content"`
	ContentTruncated      bool   `json:"content_truncated"`
	ContentOriginalLength uint32 `json:"content_original_length"`
}

type diagnoseCommandJSON struct {
	Kind              string   `json:"kind"`
	Available         bool     `json:"available"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
	TargetID          string   `json:"target_id"`
	Label             string   `json:"label"`
	Argv              []string `json:"argv"`
	Shell             string   `json:"-"`
}

type diagnoseOverLimitBreakdownJSON struct {
	TargetType                  string                `json:"target_type"`
	TargetID                    string                `json:"target_id"`
	Label                       string                `json:"label"`
	Status                      string                `json:"status"`
	FailedProblemCandidateCount uint32                `json:"failed_problem_candidate_count"`
	NextCommands                []diagnoseCommandJSON `json:"next_commands"`
}

func buildDiagnoseJSON(resp *civ1.GetFailureDiagnosisResponse, commandOrgID string) diagnoseJSONDocument {
	capabilities := resp.GetCommandCapabilities()
	out := diagnoseJSONDocument{
		OrgID:                  resp.GetOrgId(),
		State:                  diagnosisStateString(resp.GetState()),
		EmptyReason:            resp.GetEmptyReason(),
		Target:                 buildDiagnoseTargetJSON(resp.GetTarget()),
		Context:                buildDiagnoseContextJSON(resp.GetContext()),
		CommandCapabilities:    diagnoseCommandCapabilitiesJSON{SummaryCommandAvailable: capabilities.GetSummaryCommandAvailable()},
		Bounds:                 buildDiagnoseBoundsJSON(resp.GetBounds()),
		FailureGroups:          make([]diagnoseFailureGroupJSON, 0, len(resp.GetFailureGroups())),
		RepresentativeAttempts: make([]diagnoseRepresentativeJSON, 0, len(resp.GetRepresentativeAttempts())),
		NextCommands:           buildDiagnoseCommandJSONs(resp.GetNextCommands(), capabilities, commandOrgID, false),
		OverLimitBreakdown:     make([]diagnoseOverLimitBreakdownJSON, 0, len(resp.GetOverLimitBreakdown())),
	}
	for _, group := range resp.GetFailureGroups() {
		out.FailureGroups = append(out.FailureGroups, buildFailureGroupJSON(group, capabilities, commandOrgID))
	}
	for _, representative := range resp.GetRepresentativeAttempts() {
		out.RepresentativeAttempts = append(out.RepresentativeAttempts, buildRepresentativeJSON(representative, capabilities, commandOrgID))
	}
	for _, row := range resp.GetOverLimitBreakdown() {
		out.OverLimitBreakdown = append(out.OverLimitBreakdown, diagnoseOverLimitBreakdownJSON{
			TargetType:                  diagnosisTargetTypeString(row.GetTargetType()),
			TargetID:                    row.GetTargetId(),
			Label:                       row.GetLabel(),
			Status:                      diagnosisResourceStatusString(row.GetStatus()),
			FailedProblemCandidateCount: row.GetFailedProblemCandidateCount(),
			NextCommands:                buildDiagnoseCommandJSONs(row.GetNextCommands(), capabilities, commandOrgID, false),
		})
	}
	return out
}

func buildDiagnoseTargetJSON(target *civ1.FailureDiagnosisTarget) diagnoseTargetJSON {
	if target == nil {
		return diagnoseTargetJSON{TargetType: "unspecified"}
	}
	return diagnoseTargetJSON{
		TargetID:   target.GetTargetId(),
		TargetType: diagnosisTargetTypeString(target.GetTargetType()),
		Status:     diagnosisResourceStatusString(target.GetStatus()),
	}
}

func buildDiagnoseContextJSON(context *civ1.FailureDiagnosisContext) diagnoseContextJSON {
	if context == nil {
		return diagnoseContextJSON{TruncatedContextFields: []string{}}
	}
	return diagnoseContextJSON{
		RunID:                  context.GetRunId(),
		Repo:                   context.GetRepo(),
		Ref:                    context.GetRef(),
		Sha:                    context.GetSha(),
		HeadSha:                context.GetHeadSha(),
		Trigger:                context.GetTrigger(),
		RunStatus:              diagnosisResourceStatusString(context.GetRunStatus()),
		WorkflowID:             context.GetWorkflowId(),
		WorkflowName:           context.GetWorkflowName(),
		WorkflowPath:           context.GetWorkflowPath(),
		WorkflowStatus:         diagnosisResourceStatusString(context.GetWorkflowStatus()),
		JobID:                  context.GetJobId(),
		JobKey:                 context.GetJobKey(),
		JobDisplayName:         context.GetJobDisplayName(),
		JobStatus:              diagnosisResourceStatusString(context.GetJobStatus()),
		JobConclusion:          diagnosisConclusionString(context.GetJobConclusion()),
		AttemptID:              context.GetAttemptId(),
		Attempt:                context.GetAttempt(),
		AttemptStatus:          diagnosisResourceStatusString(context.GetAttemptStatus()),
		AttemptConclusion:      diagnosisConclusionString(context.GetAttemptConclusion()),
		TruncatedContextFields: append([]string(nil), context.GetTruncatedContextFields()...),
	}
}

func buildDiagnoseBoundsJSON(bounds *civ1.FailureDiagnosisBounds) diagnoseBoundsJSON {
	if bounds == nil {
		return diagnoseBoundsJSON{}
	}
	return diagnoseBoundsJSON{
		FailedProblemCandidateCount:     bounds.GetFailedProblemCandidateCount(),
		FailedProblemCandidateCap:       bounds.GetFailedProblemCandidateCap(),
		TotalProblemJobCount:            bounds.GetTotalProblemJobCount(),
		SkippedDependentCount:           bounds.GetSkippedDependentCount(),
		TotalFailureGroupCount:          bounds.GetTotalFailureGroupCount(),
		OmittedFailureGroupCount:        bounds.GetOmittedFailureGroupCount(),
		FailureGroupLimit:               bounds.GetFailureGroupLimit(),
		RepresentativesPerGroupLimit:    bounds.GetRepresentativesPerGroupLimit(),
		RecentAttemptLimit:              bounds.GetRecentAttemptLimit(),
		TotalAttemptCount:               bounds.GetTotalAttemptCount(),
		OmittedAttemptCount:             bounds.GetOmittedAttemptCount(),
		RelevantLineLimit:               bounds.GetRelevantLineLimit(),
		ErrorLineBodyCharLimit:          bounds.GetErrorLineBodyCharLimit(),
		ErrorMessageCharLimit:           bounds.GetErrorMessageCharLimit(),
		ContextLabelCharLimit:           bounds.GetContextLabelCharLimit(),
		OverLimitWorkflowBreakdownLimit: bounds.GetOverLimitWorkflowBreakdownLimit(),
		OverLimitJobBreakdownLimit:      bounds.GetOverLimitJobBreakdownLimit(),
		OmittedWorkflowBreakdownCount:   bounds.GetOmittedWorkflowBreakdownCount(),
		OmittedJobBreakdownCount:        bounds.GetOmittedJobBreakdownCount(),
		Truncated:                       bounds.GetTruncated(),
	}
}

func buildFailureGroupJSON(group *civ1.FailureGroup, capabilities *civ1.FailureDiagnosisCommandCapabilities, commandOrgID string) diagnoseFailureGroupJSON {
	out := diagnoseFailureGroupJSON{
		Fingerprint:                group.GetFingerprint(),
		Source:                     group.GetSource(),
		Count:                      group.GetCount(),
		ErrorMessage:               group.GetErrorMessage(),
		ErrorMessageTruncated:      group.GetErrorMessageTruncated(),
		ErrorMessageOriginalLength: group.GetErrorMessageOriginalLength(),
		Diagnosis:                  group.GetDiagnosis(),
		PossibleFix:                group.GetPossibleFix(),
		Representatives:            make([]diagnoseRepresentativeJSON, 0, len(group.GetRepresentatives())),
		OmittedRepresentativeCount: group.GetOmittedRepresentativeCount(),
	}
	for _, representative := range group.GetRepresentatives() {
		out.Representatives = append(out.Representatives, buildRepresentativeJSON(representative, capabilities, commandOrgID))
	}
	return out
}

func buildRepresentativeJSON(representative *civ1.RepresentativeAttempt, capabilities *civ1.FailureDiagnosisCommandCapabilities, commandOrgID string) diagnoseRepresentativeJSON {
	out := diagnoseRepresentativeJSON{
		RunID:                      representative.GetRunId(),
		WorkflowID:                 representative.GetWorkflowId(),
		WorkflowName:               representative.GetWorkflowName(),
		WorkflowPath:               representative.GetWorkflowPath(),
		JobID:                      representative.GetJobId(),
		JobKey:                     representative.GetJobKey(),
		JobDisplayName:             representative.GetJobDisplayName(),
		JobStatus:                  diagnosisResourceStatusString(representative.GetJobStatus()),
		JobConclusion:              diagnosisConclusionString(representative.GetJobConclusion()),
		AttemptID:                  representative.GetAttemptId(),
		Attempt:                    representative.GetAttempt(),
		AttemptStatus:              diagnosisResourceStatusString(representative.GetAttemptStatus()),
		AttemptConclusion:          diagnosisConclusionString(representative.GetAttemptConclusion()),
		ErrorMessage:               representative.GetErrorMessage(),
		ErrorMessageTruncated:      representative.GetErrorMessageTruncated(),
		ErrorMessageOriginalLength: representative.GetErrorMessageOriginalLength(),
		Diagnosis:                  representative.GetDiagnosis(),
		PossibleFix:                representative.GetPossibleFix(),
		RelevantLines:              make([]diagnoseRelevantLineJSON, 0, len(representative.GetRelevantLines())),
		NextCommands:               buildDiagnoseCommandJSONs(representative.GetNextCommands(), capabilities, commandOrgID, false),
	}
	for _, line := range representative.GetRelevantLines() {
		out.RelevantLines = append(out.RelevantLines, diagnoseRelevantLineJSON{
			StepID:                line.GetStepId(),
			LineNumber:            line.GetLineNumber(),
			Content:               line.GetContent(),
			ContentTruncated:      line.GetContentTruncated(),
			ContentOriginalLength: line.GetContentOriginalLength(),
		})
	}
	return out
}

func diagnosisTargetTypeString(value civ1.FailureDiagnosisTargetType) string {
	switch value {
	case civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_RUN:
		return "run"
	case civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_WORKFLOW:
		return "workflow"
	case civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_JOB:
		return "job"
	case civ1.FailureDiagnosisTargetType_FAILURE_DIAGNOSIS_TARGET_TYPE_ATTEMPT:
		return "attempt"
	default:
		return "unspecified"
	}
}

func diagnosisStateString(value civ1.FailureDiagnosisState) string {
	switch value {
	case civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_EMPTY:
		return "empty"
	case civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_GROUPED_FAILURES:
		return "grouped_failures"
	case civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_FOCUSED_FAILURE:
		return "focused_failure"
	case civ1.FailureDiagnosisState_FAILURE_DIAGNOSIS_STATE_OVER_LIMIT:
		return "over_limit"
	default:
		return "unspecified"
	}
}

func diagnosisResourceStatusString(value civ1.FailureDiagnosisResourceStatus) string {
	switch value {
	case civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_QUEUED:
		return "queued"
	case civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_WAITING:
		return "waiting"
	case civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_RUNNING:
		return "running"
	case civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FINISHED:
		return "finished"
	case civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_FAILED:
		return "failed"
	case civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_CANCELLED:
		return "cancelled"
	case civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_SKIPPED:
		return "skipped"
	default:
		return "unspecified"
	}
}

func diagnosisResourceStatusDisplayString(value civ1.FailureDiagnosisResourceStatus) string {
	if value == civ1.FailureDiagnosisResourceStatus_FAILURE_DIAGNOSIS_RESOURCE_STATUS_UNSPECIFIED {
		return ""
	}
	return diagnosisResourceStatusString(value)
}

func diagnosisConclusionString(value civ1.FailureDiagnosisConclusion) string {
	switch value {
	case civ1.FailureDiagnosisConclusion_FAILURE_DIAGNOSIS_CONCLUSION_SUCCESS:
		return "success"
	case civ1.FailureDiagnosisConclusion_FAILURE_DIAGNOSIS_CONCLUSION_FAILURE:
		return "failure"
	case civ1.FailureDiagnosisConclusion_FAILURE_DIAGNOSIS_CONCLUSION_CANCELLED:
		return "cancelled"
	case civ1.FailureDiagnosisConclusion_FAILURE_DIAGNOSIS_CONCLUSION_SKIPPED:
		return "skipped"
	case civ1.FailureDiagnosisConclusion_FAILURE_DIAGNOSIS_CONCLUSION_TIMED_OUT:
		return "timed_out"
	default:
		return "unspecified"
	}
}

func diagnosisConclusionDisplayString(value civ1.FailureDiagnosisConclusion) string {
	if value == civ1.FailureDiagnosisConclusion_FAILURE_DIAGNOSIS_CONCLUSION_UNSPECIFIED {
		return ""
	}
	return diagnosisConclusionString(value)
}

func diagnosisCommandKindString(value civ1.DrillDownCommandKind) string {
	switch value {
	case civ1.DrillDownCommandKind_DRILL_DOWN_COMMAND_KIND_LOGS:
		return "logs"
	case civ1.DrillDownCommandKind_DRILL_DOWN_COMMAND_KIND_SUMMARY:
		return "summary"
	case civ1.DrillDownCommandKind_DRILL_DOWN_COMMAND_KIND_DIAGNOSE_WORKFLOW:
		return "diagnose_workflow"
	case civ1.DrillDownCommandKind_DRILL_DOWN_COMMAND_KIND_DIAGNOSE_JOB:
		return "diagnose_job"
	default:
		return "unspecified"
	}
}

func shellJoin(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	for _, r := range arg {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_./:=@", r)) {
			return "'" + strings.ReplaceAll(arg, "'", `'"'"'`) + "'"
		}
	}
	return arg
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
