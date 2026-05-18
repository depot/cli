package ci

import (
	"fmt"
	"strings"

	"github.com/depot/cli/pkg/api"
)

const defaultVariantName = "default"

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

func variantScope(repos []string) string {
	if len(repos) == 0 {
		return "org-wide"
	}
	return strings.Join(repos, ",")
}

func filterSecretVariantsForList(secret api.CISecretGroup, repo, environment, branch, workflow []string) api.CISecretGroup {
	if len(repo) == 0 && len(environment) == 0 && len(branch) == 0 && len(workflow) == 0 {
		return secret
	}
	filtered := secret
	filtered.Variants = nil
	for _, variant := range secret.Variants {
		if variantAppliesToListFilter(variant.Attributes, repo, environment, branch, workflow) {
			filtered.Variants = append(filtered.Variants, variant)
		}
	}
	filtered.VariantCount = uint32(len(filtered.Variants))
	return filtered
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
