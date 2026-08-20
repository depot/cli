package ci

import (
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode"

	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
)

var internalLinearURL = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.)?linear\.app(?:/[^\s]*)?`)

type originDiagnosticFinding struct {
	workflow   string
	diagnostic *civ2.MigrationDiagnostic
}

func renderCursorOriginDiagnostics(out io.Writer, response *civ2.GetRepositoryMigrationAnalysisResponse) {
	findings := cursorOriginDiagnosticFindings(response)
	if len(findings) == 0 {
		return
	}

	fmt.Fprintln(out, "\nCursor Origin compatibility findings:")
	for _, finding := range findings {
		diagnostic := finding.diagnostic
		fmt.Fprintf(out, "\n  %s — %s\n", originDiagnosticSeverityLabel(diagnostic.GetSeverity()), originWorkflowDisplayPath(finding.workflow))
		writeOriginDiagnosticField(out, "Job", diagnostic.GetJob())
		writeOriginDiagnosticField(out, "Step", diagnostic.GetStep())
		writeOriginDiagnosticField(out, "Action", diagnostic.GetAction())
		writeOriginDiagnosticField(out, "Mode", diagnostic.GetMode())
		writeOriginDiagnosticField(out, "Missing capability", diagnostic.GetCapability())
		writeOriginDiagnosticField(out, "Explanation", diagnostic.GetMessage())
		writeOriginDiagnosticField(out, "Workaround", diagnostic.GetWorkaround())
	}
}

func cursorOriginDiagnosticFindings(response *civ2.GetRepositoryMigrationAnalysisResponse) []originDiagnosticFinding {
	if response == nil {
		return nil
	}

	var findings []originDiagnosticFinding
	for _, workflow := range response.GetWorkflows() {
		if workflow == nil || workflow.GetAnalysis() == nil {
			continue
		}
		diagnostics := append(
			append([]*civ2.MigrationDiagnostic(nil), workflow.GetAnalysis().GetBlockers()...),
			workflow.GetAnalysis().GetCaveats()...,
		)
		for _, diagnostic := range diagnostics {
			if !isCursorOriginCompatibilityDiagnostic(diagnostic) {
				continue
			}
			findings = append(findings, originDiagnosticFinding{
				workflow:   workflow.GetPath(),
				diagnostic: diagnostic,
			})
		}
	}
	return findings
}

func isCursorOriginCompatibilityDiagnostic(diagnostic *civ2.MigrationDiagnostic) bool {
	if diagnostic == nil || strings.TrimSpace(diagnostic.GetCapability()) == "" {
		return false
	}
	switch diagnostic.GetSeverity() {
	case civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_FAILS,
		civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_SILENTLY_DEGRADES,
		civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_CONDITIONALLY_UNSUPPORTED:
		return true
	default:
		return false
	}
}

func originDiagnosticSeverityLabel(severity civ2.MigrationDiagnosticSeverity) string {
	switch severity {
	case civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_FAILS:
		return "FAILS"
	case civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_SILENTLY_DEGRADES:
		return "SILENT DEGRADATION"
	case civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_CONDITIONALLY_UNSUPPORTED:
		return "CONDITIONALLY UNSUPPORTED"
	default:
		return ""
	}
}

func originWorkflowDisplayPath(source string) string {
	const sourcePrefix = ".github/workflows/"
	const destinationPrefix = ".depot/workflows/"

	source = strings.TrimSpace(source)
	name, found := strings.CutPrefix(source, sourcePrefix)
	if found && isSafeOriginWorkflowRelativePath(name) {
		return fmt.Sprintf("%s%s (from %s)", destinationPrefix, safeOriginDiagnosticText(name), safeOriginDiagnosticText(source))
	}
	return safeOriginDiagnosticText(source)
}

func isSafeOriginWorkflowRelativePath(name string) bool {
	if name == "" || path.IsAbs(name) || strings.Contains(name, "\\") || path.Clean(name) != name || strings.HasPrefix(name, "../") {
		return false
	}
	ext := path.Ext(name)
	return ext == ".yml" || ext == ".yaml"
}

func writeOriginDiagnosticField(out io.Writer, label, value string) {
	value = safeOriginDiagnosticText(value)
	if value == "" {
		return
	}
	fmt.Fprintf(out, "    %s: %s\n", label, value)
}

func safeOriginDiagnosticText(value string) string {
	value = internalLinearURL.ReplaceAllString(value, "[internal reference omitted]")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}
