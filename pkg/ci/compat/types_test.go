package compat

import (
	"encoding/json"
	"testing"
)

func TestCompatibilityRuleJSONRoundTrip(t *testing.T) {
	rule := CompatibilityRule{
		Feature:    "matrix",
		Supported:  Supported,
		Note:       "Matrix builds are fully supported",
		Suggestion: "Use matrix for parallel jobs",
	}

	data, err := json.Marshal(rule)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded CompatibilityRule
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Feature != rule.Feature || decoded.Supported != rule.Supported ||
		decoded.Note != rule.Note || decoded.Suggestion != rule.Suggestion {
		t.Errorf("Round-trip failed: got %+v, want %+v", decoded, rule)
	}
}

func TestCompatibilityIssueJSONRoundTrip(t *testing.T) {
	issue := CompatibilityIssue{
		File:       ".github/workflows/test.yml",
		Feature:    "reusable-workflows",
		Level:      Partial,
		Message:    "Reusable workflows have limited support",
		Suggestion: "Use composite actions instead",
	}

	data, err := json.Marshal(issue)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded CompatibilityIssue
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.File != issue.File || decoded.Feature != issue.Feature ||
		decoded.Level != issue.Level || decoded.Message != issue.Message ||
		decoded.Suggestion != issue.Suggestion {
		t.Errorf("Round-trip failed: got %+v, want %+v", decoded, issue)
	}
}

func TestCompatibilityReportJSONRoundTrip(t *testing.T) {
	report := CompatibilityReport{
		File: ".github/workflows/test.yml",
		Issues: []CompatibilityIssue{
			{
				File:       ".github/workflows/test.yml",
				Feature:    "matrix",
				Level:      Supported,
				Message:    "Matrix builds are supported",
				Suggestion: "Use matrix for parallel jobs",
			},
			{
				File:       ".github/workflows/test.yml",
				Feature:    "reusable-workflows",
				Level:      Partial,
				Message:    "Reusable workflows have limited support",
				Suggestion: "Use composite actions instead",
			},
		},
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded CompatibilityReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.File != report.File || len(decoded.Issues) != len(report.Issues) {
		t.Errorf("Round-trip failed: got %+v, want %+v", decoded, report)
	}

	for i, issue := range decoded.Issues {
		if issue.Feature != report.Issues[i].Feature || issue.Level != report.Issues[i].Level {
			t.Errorf("Issue %d round-trip failed: got %+v, want %+v", i, issue, report.Issues[i])
		}
	}
}

func TestSupportLevelValues(t *testing.T) {
	tests := []struct {
		level SupportLevel
		want  int
	}{
		{Supported, 0},
		{Unsupported, 1},
		{InProgress, 2},
		{Partial, 3},
	}

	for _, tt := range tests {
		if int(tt.level) != tt.want {
			t.Errorf("SupportLevel %v = %d, want %d", tt.level, int(tt.level), tt.want)
		}
	}
}
