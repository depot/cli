package ci

import (
	"bytes"
	"strings"
	"testing"

	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
)

func TestRenderCursorOriginDiagnosticsShowsStructuredFindings(t *testing.T) {
	response := &civ2.GetRepositoryMigrationAnalysisResponse{
		Workflows: []*civ2.RepositoryWorkflowMigration{
			{
				Path: ".github/workflows/ci.yml",
				Analysis: &civ2.WorkflowAnalysis{
					Blockers: []*civ2.MigrationDiagnostic{
						{
							Message:    "Cursor Origin does not provide GitHub release APIs.",
							Action:     "softprops/action-gh-release",
							Job:        "release",
							Step:       "Publish release",
							Mode:       "publish-release",
							Capability: "github-releases",
							Severity:   civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_FAILS,
							Workaround: "Publish releases from a GitHub workflow.",
						},
					},
					Caveats: []*civ2.MigrationDiagnostic{
						{
							Message:    "The action can report success without publishing results.",
							Action:     "aquasecurity/trivy-action",
							Job:        "scan",
							Step:       "Upload dependency snapshot",
							Mode:       "dependency-snapshot",
							Capability: "dependency-snapshots",
							Severity:   civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_SILENTLY_DEGRADES,
							Workaround: "Use another output format.",
						},
						{
							Message:    "Runtime input determines whether this mode is supported.",
							Action:     "github/codeql-action/analyze",
							Job:        "codeql",
							Step:       "Analyze",
							Mode:       "upload-analysis",
							Capability: "code-scanning-sarif",
							Severity:   civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_CONDITIONALLY_UNSUPPORTED,
							Workaround: "Set upload to never.",
						},
					},
				},
			},
		},
	}

	var output bytes.Buffer
	renderCursorOriginDiagnostics(&output, response)
	text := output.String()

	for _, expected := range []string{
		"Cursor Origin compatibility findings:",
		"FAILS — .depot/workflows/ci.yml (from .github/workflows/ci.yml)",
		"SILENT DEGRADATION — .depot/workflows/ci.yml (from .github/workflows/ci.yml)",
		"CONDITIONALLY UNSUPPORTED — .depot/workflows/ci.yml (from .github/workflows/ci.yml)",
		"Job: release",
		"Step: Publish release",
		"Action: softprops/action-gh-release",
		"Mode: publish-release",
		"Missing capability: github-releases",
		"Explanation: Cursor Origin does not provide GitHub release APIs.",
		"Workaround: Publish releases from a GitHub workflow.",
		"Job: scan",
		"Step: Upload dependency snapshot",
		"Action: aquasecurity/trivy-action",
		"Mode: dependency-snapshot",
		"Missing capability: dependency-snapshots",
		"Workaround: Use another output format.",
		"Job: codeql",
		"Step: Analyze",
		"Action: github/codeql-action/analyze",
		"Mode: upload-analysis",
		"Missing capability: code-scanning-sarif",
		"Workaround: Set upload to never.",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("output does not contain %q:\n%s", expected, text)
		}
	}
}

func TestRenderCursorOriginDiagnosticsOmitsGeneralAndAbsentFields(t *testing.T) {
	response := &civ2.GetRepositoryMigrationAnalysisResponse{
		Workflows: []*civ2.RepositoryWorkflowMigration{
			{
				Path: ".github/workflows/ci.yml",
				Analysis: &civ2.WorkflowAnalysis{
					Blockers: []*civ2.MigrationDiagnostic{
						{
							Code:     "workflow_parse_failed",
							Message:  "This general readiness finding must stay quiet.",
							Severity: civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_FAILS,
						},
						{
							Code:       "internal_tracking_code",
							Message:    "See https://linear.app/depot/issue/DEP-1234\nfor internal details.\x1b[31m",
							Capability: "github-graphql",
							Severity:   civ2.MigrationDiagnosticSeverity_MIGRATION_DIAGNOSTIC_SEVERITY_FAILS,
						},
					},
				},
			},
		},
	}

	var output bytes.Buffer
	renderCursorOriginDiagnostics(&output, response)
	text := output.String()

	if strings.Contains(text, "general readiness") {
		t.Fatalf("general migration diagnostic was rendered:\n%s", text)
	}
	for _, forbidden := range []string{"internal_tracking_code", "linear.app", "DEP-1234", "\x1b", "Job:", "Step:", "Action:", "Mode:", "Workaround:"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("output contains %q:\n%s", forbidden, text)
		}
	}
	if !strings.Contains(text, "Explanation: See [internal reference omitted] for internal details. [31m") {
		t.Errorf("sanitized explanation is missing:\n%s", text)
	}
}

func TestRenderCursorOriginDiagnosticsKeepsCompatibleWorkflowsQuiet(t *testing.T) {
	responses := []*civ2.GetRepositoryMigrationAnalysisResponse{
		nil,
		{},
		{Workflows: []*civ2.RepositoryWorkflowMigration{{Path: ".github/workflows/ci.yml"}}},
		{
			Workflows: []*civ2.RepositoryWorkflowMigration{{
				Path:     ".github/workflows/ci.yml",
				Analysis: &civ2.WorkflowAnalysis{},
			}},
		},
	}

	for index, response := range responses {
		var output bytes.Buffer
		renderCursorOriginDiagnostics(&output, response)
		if output.Len() != 0 {
			t.Errorf("response %d unexpectedly produced output: %q", index, output.String())
		}
	}
}
