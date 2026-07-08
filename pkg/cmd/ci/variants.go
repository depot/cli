package ci

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/depot/cli/pkg/api"
)

const defaultVariantName = "default"

// maxShadowProbes bounds how many distinct sibling scopes the write-path shadow check will probe,
// so a bulk import can't fan out into an unbounded number of follow-up requests.
const maxShadowProbes = 16

func displayVariantName(name string) string {
	if name == "" {
		return defaultVariantName
	}
	return name
}

func formatVariantAttributes(attrs []api.CIVariantAttribute) string {
	if len(attrs) == 0 {
		return "all"
	}

	parts := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		parts = append(parts, fmt.Sprintf("%s=%s", attr.Key, attr.Value))
	}
	return strings.Join(parts, ",")
}

// variantStatusLabel turns a server-computed resolution into a short column value for the list table.
// It is only meaningful when the list request carried a job context; without context the resolution is
// empty and this returns "-".
func variantStatusLabel(resolution string) string {
	switch resolution {
	case "resolved":
		return "active (wins)"
	case "lower-priority":
		return "shadowed"
	case "indeterminate":
		return "may win (needs branch/env)"
	default:
		return "-"
	}
}

// contextFromAttributes splits stored variant attributes back into the per-dimension selector slices
// that the list RPC accepts as a job context.
func contextFromAttributes(attrs []api.CIVariantAttribute) (repo, environment, branch, workflow []string) {
	for _, attr := range attrs {
		switch attr.Key {
		case "repository":
			repo = append(repo, attr.Value)
		case "environment":
			environment = append(environment, attr.Value)
		case "branch":
			branch = append(branch, attr.Value)
		case "workflow":
			workflow = append(workflow, attr.Value)
		}
	}
	return repo, environment, branch, workflow
}

// selectorHintFromAttributes renders a variant's scope as the flags a user would pass to reproduce it,
// e.g. `--repo owner/repo --env production`. Used to point at the exact `secrets list` invocation that
// reveals a shadowing variant.
func selectorHintFromAttributes(attrs []api.CIVariantAttribute) string {
	flagFor := map[string]string{
		"repository":  "--repo",
		"environment": "--env",
		"branch":      "--branch",
		"workflow":    "--workflow",
	}
	parts := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		if flag, ok := flagFor[attr.Key]; ok {
			parts = append(parts, fmt.Sprintf("%s %s", flag, attr.Value))
		}
	}
	return strings.Join(parts, " ")
}

// warnSecretVariantShadowed prints an advisory to stderr when the secret variant that was just written
// is overridden by a more specific variant in some job context. This closes the "reported success but
// nothing changed" gap — e.g. re-importing the default value while a repository-scoped variant keeps
// winning for that repository.
//
// Precedence stays server-authoritative: for each sibling variant's scope we ask the server (via a
// context-aware list) which variant wins there, and only warn when the server marks the variant we
// just wrote as lower-priority. The CLI never recomputes the specificity weights. The write has
// already succeeded, so any failure of the follow-up read is swallowed rather than surfaced.
func warnSecretVariantShadowed(ctx context.Context, token, orgID string, res api.CISetSecretVariantResult) {
	written := res.Variant
	secretName := res.Secret.Name

	// Probe each distinct, non-empty sibling scope. A sibling with no attributes is the catch-all
	// default, which can never shadow another variant by specificity, so skip it. For a sibling
	// that constrains a dimension the written variant also constrains with a different value, the
	// two can never apply to the same job, so skip it too (no shadow is possible).
	writtenByKey := map[string]map[string]bool{}
	for _, attr := range written.Attributes {
		if writtenByKey[attr.Key] == nil {
			writtenByKey[attr.Key] = map[string]bool{}
		}
		writtenByKey[attr.Key][attr.Value] = true
	}

	seen := map[string]bool{}
	shadowerScopes := make([]string, 0)
	probes := 0
	for _, sibling := range res.Secret.Variants {
		if sibling.ID == written.ID || len(sibling.Attributes) == 0 {
			continue
		}
		if scopesDisjoint(writtenByKey, sibling.Attributes) {
			continue
		}

		// Build the probe context: the written variant's own scope, widened with any dimension the
		// sibling constrains that the written variant leaves open. This finds the job where both
		// could apply and lets the server decide which wins.
		probeAttrs := mergeScopes(written.Attributes, sibling.Attributes)
		repo, environment, branch, workflow := contextFromAttributes(probeAttrs)
		key := scopeKey(repo, environment, branch, workflow)
		if seen[key] {
			continue
		}
		seen[key] = true
		if probes >= maxShadowProbes {
			break
		}
		probes++

		result, err := api.CIListSecretVariants(ctx, token, orgID, api.CIListSecretVariantsOptions{
			Query:       secretName,
			Repo:        repo,
			Environment: environment,
			Branch:      branch,
			Workflow:    workflow,
		})
		if err != nil {
			return
		}
		if winner, ok := shadowingWinner(result.Secrets, secretName, written.ID); ok {
			shadowerScopes = append(shadowerScopes, selectorHintFromAttributes(winner.Attributes))
		}
	}

	if len(shadowerScopes) == 0 {
		return
	}

	hint := shadowerScopes[0]
	if hint == "" {
		hint = "--repo owner/repo"
	}
	fmt.Fprintf(os.Stderr, "Warning: the value you set for %q is shadowed for some jobs by a more specific variant.\n", secretName)
	fmt.Fprintf(os.Stderr, "         Run `depot ci secrets list %s %s` to see which variant wins.\n", secretName, hint)
}

// scopesDisjoint reports whether a sibling's attributes contradict the written variant on any shared
// dimension, meaning no single job can match both. A dimension only contradicts when the two constrain
// it to entirely different value sets; attributes are repeatable, so a shared dimension with any
// overlapping value (an OR within the dimension) still lets both apply.
func scopesDisjoint(writtenByKey map[string]map[string]bool, siblingAttrs []api.CIVariantAttribute) bool {
	siblingByKey := map[string][]string{}
	for _, attr := range siblingAttrs {
		siblingByKey[attr.Key] = append(siblingByKey[attr.Key], attr.Value)
	}
	for key, siblingValues := range siblingByKey {
		writtenValues, ok := writtenByKey[key]
		if !ok {
			continue // the written variant leaves this dimension open; the sibling only widens it
		}
		overlap := false
		for _, value := range siblingValues {
			if writtenValues[value] {
				overlap = true
				break
			}
		}
		if !overlap {
			return true
		}
	}
	return false
}

// mergeScopes returns the written variant's attributes plus any dimension the sibling constrains that
// the written variant leaves open.
func mergeScopes(writtenAttrs, siblingAttrs []api.CIVariantAttribute) []api.CIVariantAttribute {
	constrained := map[string]bool{}
	merged := make([]api.CIVariantAttribute, 0, len(writtenAttrs)+len(siblingAttrs))
	for _, attr := range writtenAttrs {
		constrained[attr.Key] = true
		merged = append(merged, attr)
	}
	for _, attr := range siblingAttrs {
		if !constrained[attr.Key] {
			merged = append(merged, attr)
		}
	}
	return merged
}

// shadowingWinner finds, within the server's context-resolved response, the variant that wins over the
// just-written variant when that written variant came back lower-priority.
func shadowingWinner(secrets []api.CISecretGroup, secretName, writtenID string) (api.CISecretVariant, bool) {
	for _, group := range secrets {
		if !strings.EqualFold(group.Name, secretName) {
			continue
		}
		shadowed := false
		var winner api.CISecretVariant
		haveWinner := false
		for _, variant := range group.Variants {
			if variant.ID == writtenID && variant.Resolution == "lower-priority" {
				shadowed = true
			}
			if variant.Resolution == "resolved" {
				winner = variant
				haveWinner = true
			}
		}
		if shadowed && haveWinner {
			return winner, true
		}
	}
	return api.CISecretVariant{}, false
}

func scopeKey(repo, environment, branch, workflow []string) string {
	return strings.Join(repo, ",") + "|" + strings.Join(environment, ",") + "|" + strings.Join(branch, ",") + "|" + strings.Join(workflow, ",")
}

func variantScope(repos []string) string {
	if len(repos) == 0 {
		return "org-wide"
	}
	return strings.Join(repos, ",")
}

func resolveSecretVariant(group api.CISecretGroup, variant string, repo, environment, branch, workflow []string) ([]api.CISecretVariant, error) {
	matches := make([]api.CISecretVariant, 0, len(group.Variants))
	for _, candidate := range group.Variants {
		if variant != "" && candidate.Name != variant {
			continue
		}
		if !variantAttributesMatch(candidate.Attributes, repo, environment, branch, workflow) {
			continue
		}
		matches = append(matches, candidate)
	}
	return matches, nil
}

func resolveVariableVariant(group api.CIVariableGroup, variant string, repo, environment, branch, workflow []string) ([]api.CIVariableVariant, error) {
	matches := make([]api.CIVariableVariant, 0, len(group.Variants))
	for _, candidate := range group.Variants {
		if variant != "" && candidate.Name != variant {
			continue
		}
		if !variantAttributesMatch(candidate.Attributes, repo, environment, branch, workflow) {
			continue
		}
		matches = append(matches, candidate)
	}
	return matches, nil
}

func filterVariableVariantsForList(variable api.CIVariableGroup, repo, environment, branch, workflow []string) api.CIVariableGroup {
	if len(repo) == 0 && len(environment) == 0 && len(branch) == 0 && len(workflow) == 0 {
		return variable
	}
	filtered := variable
	filtered.Variants = nil
	for _, variant := range variable.Variants {
		if variantAppliesToListFilter(variant.Attributes, repo, environment, branch, workflow) {
			filtered.Variants = append(filtered.Variants, variant)
		}
	}
	filtered.VariantCount = uint32(len(filtered.Variants))
	return filtered
}

func hasVariantSelectors(repo, environment, branch, workflow []string) bool {
	return hasNonEmpty(repo) || hasNonEmpty(environment) || hasNonEmpty(branch) || hasNonEmpty(workflow)
}

func hasNonEmpty(values []string) bool {
	for _, value := range values {
		if value != "" {
			return true
		}
	}
	return false
}

func legacyListRepoSelector(repo, environment, branch, workflow []string) (string, bool) {
	if hasNonEmpty(environment) || hasNonEmpty(branch) || hasNonEmpty(workflow) {
		return "", false
	}
	nonEmptyRepos := nonEmptyValues(repo)
	if len(nonEmptyRepos) > 1 {
		return "", false
	}
	if len(nonEmptyRepos) == 1 {
		return nonEmptyRepos[0], true
	}
	return "", true
}

func nonEmptyValues(values []string) []string {
	nonEmpty := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			nonEmpty = append(nonEmpty, value)
		}
	}
	return nonEmpty
}

func variantAppliesToListFilter(attrs []api.CIVariantAttribute, repos, environments, branches, workflows []string) bool {
	expected := map[string][]string{}
	addExpected := func(key string, values []string) {
		for _, value := range values {
			if value != "" {
				expected[key] = append(expected[key], value)
			}
		}
	}
	addExpected("repository", repos)
	addExpected("environment", environments)
	addExpected("branch", branches)
	addExpected("workflow", workflows)
	if len(expected) == 0 || len(attrs) == 0 {
		return true
	}

	attributesByKey := map[string][]string{}
	for _, attr := range attrs {
		attributesByKey[attr.Key] = append(attributesByKey[attr.Key], attr.Value)
	}

	for key, wants := range expected {
		values, ok := attributesByKey[key]
		if !ok {
			continue
		}
		if !anyAttributeValueMatches(key, values, wants) {
			return false
		}
	}
	return true
}

func variantAttributesMatch(attrs []api.CIVariantAttribute, repos, environments, branches, workflows []string) bool {
	expected := map[string][]string{}
	addExpected := func(key string, values []string) {
		for _, value := range values {
			if value != "" {
				expected[key] = append(expected[key], value)
			}
		}
	}
	addExpected("repository", repos)
	addExpected("environment", environments)
	addExpected("branch", branches)
	addExpected("workflow", workflows)
	if len(expected) == 0 {
		return true
	}

	matched := map[string]bool{}
	for _, attr := range attrs {
		wants, ok := expected[attr.Key]
		if !ok {
			continue
		}
		if anyAttributeValueMatches(attr.Key, []string{attr.Value}, wants) {
			matched[attr.Key] = true
		}
	}
	return len(matched) == len(expected)
}

func anyAttributeValueMatches(key string, values, wants []string) bool {
	for _, value := range values {
		for _, want := range wants {
			if key == "repository" && strings.EqualFold(want, value) {
				return true
			}
			if key != "repository" && want == value {
				return true
			}
		}
	}
	return false
}
