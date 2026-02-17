package compat

import (
	"strings"
	"testing"
)

func TestSupportedTriggersAreMarkedSupported(t *testing.T) {
	supported := []string{
		"push",
		"pull_request",
		"pull_request_target",
		"schedule",
		"workflow_call",
		"workflow_dispatch",
		"workflow_run",
	}

	for _, trigger := range supported {
		rule, ok := TriggerRules[trigger]
		if !ok {
			t.Fatalf("missing trigger rule for %q", trigger)
		}
		if rule.Supported != Supported {
			t.Fatalf("expected %q to be supported, got %v", trigger, rule.Supported)
		}
	}
}

func TestUnsupportedTriggersAreMarkedUnsupported(t *testing.T) {
	unsupported := []string{
		"branch_protection_rule",
		"check_run",
		"check_suite",
		"create",
		"delete",
		"deployment",
		"deployment_status",
		"discussion",
		"discussion_comment",
		"fork",
		"gollum",
		"issue_comment",
		"issues",
		"label",
		"merge_group",
		"milestone",
		"page_build",
		"project",
		"project_card",
		"project_column",
		"public",
		"registry_package",
		"release",
		"repository_dispatch",
		"status",
		"watch",
	}

	for _, trigger := range unsupported {
		rule, ok := TriggerRules[trigger]
		if !ok {
			t.Fatalf("missing trigger rule for %q", trigger)
		}
		if rule.Supported != Unsupported {
			t.Fatalf("expected %q to be unsupported, got %v", trigger, rule.Supported)
		}
	}
}

func TestUnsupportedRulesHaveSuggestion(t *testing.T) {
	allRuleSets := []map[string]CompatibilityRule{
		TriggerRules,
		JobFeatureRules,
		StepFeatureRules,
		ExpressionRules,
	}

	for _, ruleSet := range allRuleSets {
		for key, rule := range ruleSet {
			if rule.Supported != Unsupported {
				continue
			}

			if strings.TrimSpace(rule.Suggestion) == "" {
				t.Fatalf("rule %q is unsupported but has no suggestion", key)
			}
		}
	}
}
