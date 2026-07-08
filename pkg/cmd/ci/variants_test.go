package ci

import (
	"testing"

	"github.com/depot/cli/pkg/api"
)

func TestVariantStatusLabel(t *testing.T) {
	cases := map[string]string{
		"resolved":       "active (wins)",
		"lower-priority": "shadowed",
		"indeterminate":  "may win (needs branch/env)",
		"":               "-",
		"unknown":        "-",
	}
	for resolution, want := range cases {
		if got := variantStatusLabel(resolution); got != want {
			t.Errorf("variantStatusLabel(%q) = %q, want %q", resolution, got, want)
		}
	}
}

func TestContextFromAttributes(t *testing.T) {
	repo, env, branch, workflow := contextFromAttributes([]api.CIVariantAttribute{
		{Key: "repository", Value: "owner/repo"},
		{Key: "environment", Value: "production"},
		{Key: "branch", Value: "main"},
		{Key: "workflow", Value: "deploy.yml"},
		{Key: "unknown", Value: "ignored"},
	})
	if len(repo) != 1 || repo[0] != "owner/repo" {
		t.Errorf("repo = %v", repo)
	}
	if len(env) != 1 || env[0] != "production" {
		t.Errorf("env = %v", env)
	}
	if len(branch) != 1 || branch[0] != "main" {
		t.Errorf("branch = %v", branch)
	}
	if len(workflow) != 1 || workflow[0] != "deploy.yml" {
		t.Errorf("workflow = %v", workflow)
	}
}

func TestSelectorHintFromAttributes(t *testing.T) {
	got := selectorHintFromAttributes([]api.CIVariantAttribute{
		{Key: "repository", Value: "owner/repo"},
		{Key: "environment", Value: "production"},
	})
	want := "--repo owner/repo --env production"
	if got != want {
		t.Errorf("selectorHintFromAttributes = %q, want %q", got, want)
	}
	if selectorHintFromAttributes(nil) != "" {
		t.Errorf("empty attributes should yield empty hint")
	}
}

func TestScopesDisjoint(t *testing.T) {
	written := map[string]map[string]bool{
		"repository": {"owner/repo": true},
	}

	// Sibling constrains the same repository with a different value: disjoint, no job matches both.
	if !scopesDisjoint(written, []api.CIVariantAttribute{{Key: "repository", Value: "owner/other"}}) {
		t.Error("different repository values should be disjoint")
	}
	// Sibling constrains a dimension the written variant leaves open: not disjoint.
	if scopesDisjoint(written, []api.CIVariantAttribute{{Key: "environment", Value: "production"}}) {
		t.Error("widening a new dimension should not be disjoint")
	}
	// Sibling agrees on the shared dimension: not disjoint.
	if scopesDisjoint(written, []api.CIVariantAttribute{{Key: "repository", Value: "owner/repo"}}) {
		t.Error("matching repository values should not be disjoint")
	}
	// Repeatable values on a shared dimension: overlap on any value means not disjoint.
	if scopesDisjoint(written, []api.CIVariantAttribute{
		{Key: "repository", Value: "owner/repo"},
		{Key: "repository", Value: "owner/other"},
		{Key: "branch", Value: "main"},
	}) {
		t.Error("a sibling that includes the written repository among several should not be disjoint")
	}
	// Shared dimension with no overlapping value: disjoint.
	if !scopesDisjoint(written, []api.CIVariantAttribute{
		{Key: "repository", Value: "owner/a"},
		{Key: "repository", Value: "owner/b"},
	}) {
		t.Error("a sibling constraining repository to entirely different values should be disjoint")
	}
}

func TestMergeScopes(t *testing.T) {
	written := []api.CIVariantAttribute{{Key: "repository", Value: "owner/repo"}}
	sibling := []api.CIVariantAttribute{
		{Key: "repository", Value: "owner/repo"},  // already constrained: not duplicated
		{Key: "environment", Value: "production"}, // new dimension: added
	}
	merged := mergeScopes(written, sibling)
	if len(merged) != 2 {
		t.Fatalf("merged = %v, want 2 attributes", merged)
	}
	seen := map[string]string{}
	for _, attr := range merged {
		seen[attr.Key] = attr.Value
	}
	if seen["repository"] != "owner/repo" || seen["environment"] != "production" {
		t.Errorf("merged scope = %v", seen)
	}
}

func TestShadowingWinner(t *testing.T) {
	secrets := []api.CISecretGroup{{
		Name: "NPM_TOKEN",
		Variants: []api.CISecretVariant{
			{ID: "written", Name: "default", Resolution: "lower-priority"},
			{ID: "repo", Name: "default", Attributes: []api.CIVariantAttribute{{Key: "repository", Value: "owner/repo"}}, Resolution: "resolved"},
		},
	}}

	winner, ok := shadowingWinner(secrets, "NPM_TOKEN", "written")
	if !ok {
		t.Fatal("expected the written variant to be reported as shadowed")
	}
	if winner.ID != "repo" {
		t.Errorf("winner = %q, want repo", winner.ID)
	}

	// When the written variant is the resolved winner, nothing shadows it.
	secrets[0].Variants[0].Resolution = "resolved"
	secrets[0].Variants[1].Resolution = "lower-priority"
	if _, ok := shadowingWinner(secrets, "NPM_TOKEN", "written"); ok {
		t.Error("a winning written variant should not be reported as shadowed")
	}

	// When resolution is indeterminate (missing context), we cannot claim a definite winner.
	secrets[0].Variants[0].Resolution = "indeterminate"
	secrets[0].Variants[1].Resolution = "indeterminate"
	if _, ok := shadowingWinner(secrets, "NPM_TOKEN", "written"); ok {
		t.Error("indeterminate resolution should not report shadowing")
	}
}
