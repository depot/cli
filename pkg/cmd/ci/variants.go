package ci

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/depot/cli/pkg/api"
)

const defaultVariantName = "default"

// maxShadowProbes bounds follow-up requests made by a write-path shadow check.
const maxShadowProbes = 16

// shadowProbeBudget shares a probe limit across bulk writes; nil leaves the per-write limit in place.
type shadowProbeBudget struct {
	remaining int
}

func newShadowProbeBudget(n int) *shadowProbeBudget {
	return &shadowProbeBudget{remaining: n}
}

// take reserves up to n probes and returns the number allowed.
func (b *shadowProbeBudget) take(n int) int {
	if b == nil {
		return n
	}
	if b.remaining <= 0 {
		return 0
	}
	if n > b.remaining {
		n = b.remaining
	}
	b.remaining -= n
	return n
}

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

// variantStatusLabel maps the server's resolution to the list-table label.
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

// contextFromAttributes converts stored attributes into list-RPC context selectors.
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

// selectorHintFromAttributes renders a variant scope as list-command flags.
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

// warnSecretVariantShadowed warns when the just-written variant is shadowed. Resolution stays
// server-authoritative, and probe failures are ignored because the write already succeeded.
func warnSecretVariantShadowed(ctx context.Context, token, orgID string, res api.CISetSecretVariantResult, budget *shadowProbeBudget) {
	written := res.Variant
	secretName := res.Secret.Name

	siblings := make([][]api.CIVariantAttribute, 0, len(res.Secret.Variants))
	for _, sibling := range res.Secret.Variants {
		if sibling.ID == written.ID {
			continue
		}
		siblings = append(siblings, sibling.Attributes)
	}

	probes := shadowProbeContexts(written.Attributes, siblings)
	probes = probes[:budget.take(len(probes))]
	shadowerScopes := make([]string, 0)
	for _, probeAttrs := range probes {
		repo, environment, branch, workflow := contextFromAttributes(probeAttrs)
		result, err := api.CIListSecretVariants(ctx, token, orgID, api.CIListSecretVariantsOptions{
			Query:       secretName,
			Repo:        repo,
			Environment: environment,
			Branch:      branch,
			Workflow:    workflow,
		})
		if err != nil {
			// The write already succeeded; a transient read failure on one probe should not suppress
			// the shadow warnings that other probes might still surface, so skip this one.
			continue
		}
		if winner, ok := shadowingWinner(result.Secrets, secretName, written.ID); ok {
			shadowerScopes = append(shadowerScopes, selectorHintFromAttributes(winner.Attributes))
		}
	}

	emitShadowWarning("secrets", secretName, shadowerScopes)
}

// warnVariableVariantShadowed is the variable counterpart; variables use group-level resolution and
// winner-first ordering instead of per-variant resolution.
func warnVariableVariantShadowed(ctx context.Context, token, orgID string, res api.CISetVariableVariantResult, budget *shadowProbeBudget) {
	written := res.Variant
	varName := res.Variable.Name

	siblings := make([][]api.CIVariantAttribute, 0, len(res.Variable.Variants))
	for _, sibling := range res.Variable.Variants {
		if sibling.ID == written.ID {
			continue
		}
		siblings = append(siblings, sibling.Attributes)
	}

	probes := shadowProbeContexts(written.Attributes, siblings)
	probes = probes[:budget.take(len(probes))]
	shadowerScopes := make([]string, 0)
	for _, probeAttrs := range probes {
		repo, environment, branch, workflow := contextFromAttributes(probeAttrs)
		result, err := api.CIListVariableVariants(ctx, token, orgID, api.CIListVariableVariantsOptions{
			Query:       varName,
			Repo:        repo,
			Environment: environment,
			Branch:      branch,
			Workflow:    workflow,
		})
		if err != nil {
			continue
		}
		if winner, ok := variableShadowingWinner(result.Variables, varName, written.ID); ok {
			shadowerScopes = append(shadowerScopes, selectorHintFromAttributes(winner.Attributes))
		}
	}

	emitShadowWarning("vars", varName, shadowerScopes)
}

// shadowProbeContexts returns distinct, bounded contexts for siblings that could overlap the write.
// The written scope is widened only by dimensions the sibling adds; catch-all and disjoint siblings
// cannot shadow it.
func shadowProbeContexts(written []api.CIVariantAttribute, siblings [][]api.CIVariantAttribute) [][]api.CIVariantAttribute {
	writtenByKey := map[string]map[string]bool{}
	for _, attr := range written {
		if writtenByKey[attr.Key] == nil {
			writtenByKey[attr.Key] = map[string]bool{}
		}
		writtenByKey[attr.Key][normalizeScopeValue(attr.Key, attr.Value)] = true
	}

	seen := map[string]bool{}
	probes := make([][]api.CIVariantAttribute, 0)
	for _, siblingAttrs := range siblings {
		if len(siblingAttrs) == 0 {
			continue
		}
		if scopesDisjoint(writtenByKey, siblingAttrs) {
			continue
		}
		probeAttrs := mergeScopes(written, siblingAttrs)
		repo, environment, branch, workflow := contextFromAttributes(probeAttrs)
		key := scopeKey(repo, environment, branch, workflow)
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(probes) >= maxShadowProbes {
			break
		}
		probes = append(probes, probeAttrs)
	}
	return probes
}

// emitShadowWarning prints the list command that reveals the shadowing winner.
func emitShadowWarning(kind, name string, shadowerScopes []string) {
	if len(shadowerScopes) == 0 {
		return
	}
	hint := shadowerScopes[0]
	if hint == "" {
		hint = "--repo owner/repo"
	}
	fmt.Fprintf(os.Stderr, "Warning: the value you set for %q is shadowed for some jobs by a more specific variant.\n", name)
	fmt.Fprintf(os.Stderr, "         Run `depot ci %s list %s %s` to see which variant wins.\n", kind, name, hint)
}

// scopesDisjoint reports whether no job can match both scopes. Repeatable values overlap when any value
// matches; patterns remain potentially overlapping so the probe errs toward asking the server.
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
			if writtenValues[normalizeScopeValue(key, value)] || looksLikePattern(value) {
				overlap = true
				break
			}
		}
		if !overlap {
			for writtenValue := range writtenValues {
				if looksLikePattern(writtenValue) {
					overlap = true
					break
				}
			}
		}
		if !overlap {
			return true
		}
	}
	return false
}

// looksLikePattern reports whether a selector may contain glob syntax.
func looksLikePattern(value string) bool {
	return strings.ContainsAny(value, "*?[]{}")
}

// normalizeScopeValue applies the same case-insensitive comparison used for repositories elsewhere.
func normalizeScopeValue(key, value string) string {
	if key == "repository" {
		return strings.ToLower(value)
	}
	return value
}

// mergeScopes adds dimensions constrained only by the sibling.
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

// shadowingWinner returns the server-reported winner when the write is lower priority.
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

// variableShadowingWinner uses a resolved group's winner-first ordering because variables report
// resolution only at group level.
func variableShadowingWinner(variables []api.CIVariableGroup, name, writtenID string) (api.CIVariableVariant, bool) {
	for _, group := range variables {
		if !strings.EqualFold(group.Name, name) {
			continue
		}
		if group.Resolution != "resolved" || len(group.Variants) == 0 {
			continue
		}
		top := group.Variants[0]
		if top.ID == writtenID {
			continue // the written variant is itself the winner
		}
		for _, variant := range group.Variants {
			if variant.ID == writtenID {
				return top, true
			}
		}
	}
	return api.CIVariableVariant{}, false
}

// variableVariantRowResolution derives row status from group resolution and winner-first ordering.
func variableVariantRowResolution(groupResolution string, index int) string {
	switch groupResolution {
	case "resolved":
		if index == 0 {
			return "resolved"
		}
		return "lower-priority"
	case "indeterminate":
		return "indeterminate"
	default:
		return ""
	}
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
