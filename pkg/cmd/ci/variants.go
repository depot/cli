package ci

import (
	"context"
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

// findSecretVariants returns the secret's variants that the selectors pick, as the server resolves them (a repository
// by the id holding its name now, DEP-7549), narrowed to the variant name when one is given.
func findSecretVariants(ctx context.Context, token, orgID, name, variant string, repo, environment, branch, workflow []string) ([]api.CISecretVariant, error) {
	var group api.CISecretGroup
	var err error
	if hasVariantSelectors(repo, environment, branch, workflow) {
		group, _, err = api.CIFindSecretVariantGroup(ctx, token, orgID, name, api.CIListSecretVariantsOptions{
			Repo: repo, Environment: environment, Branch: branch, Workflow: workflow,
		})
	} else {
		group, err = api.CIGetSecretVariantGroup(ctx, token, orgID, name)
	}
	if err != nil {
		return nil, err
	}
	matches := make([]api.CISecretVariant, 0, len(group.Variants))
	for _, candidate := range group.Variants {
		if variant == "" || candidate.Name == variant {
			matches = append(matches, candidate)
		}
	}
	return matches, nil
}

// findVariableVariants is findSecretVariants for CI variables.
func findVariableVariants(ctx context.Context, token, orgID, name, variant string, repo, environment, branch, workflow []string) ([]api.CIVariableVariant, error) {
	var group api.CIVariableGroup
	var err error
	if hasVariantSelectors(repo, environment, branch, workflow) {
		group, _, err = api.CIFindVariableVariantGroup(ctx, token, orgID, name, api.CIListVariableVariantsOptions{
			Repo: repo, Environment: environment, Branch: branch, Workflow: workflow,
		})
	} else {
		group, err = api.CIGetVariableVariantGroup(ctx, token, orgID, name)
	}
	if err != nil {
		return nil, err
	}
	matches := make([]api.CIVariableVariant, 0, len(group.Variants))
	for _, candidate := range group.Variants {
		if variant == "" || candidate.Name == variant {
			matches = append(matches, candidate)
		}
	}
	return matches, nil
}
