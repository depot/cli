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

	siblings := make([][]api.CIVariantAttribute, 0, len(res.Secret.Variants))
	for _, sibling := range res.Secret.Variants {
		if sibling.ID == written.ID {
			continue
		}
		siblings = append(siblings, sibling.Attributes)
	}

	shadowerScopes := make([]string, 0)
	for _, probeAttrs := range shadowProbeContexts(written.Attributes, siblings) {
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

// warnVariableVariantShadowed is the variable-command counterpart of warnSecretVariantShadowed. It
// works the same way — probe each sibling scope and let the server decide the winner — but variables
// report resolution only at the group level, so shadowing is read from the server's winner-first
// ordering rather than a per-row lower-priority flag (see variableShadowingWinner).
func warnVariableVariantShadowed(ctx context.Context, token, orgID string, res api.CISetVariableVariantResult) {
	written := res.Variant
	varName := res.Variable.Name

	siblings := make([][]api.CIVariantAttribute, 0, len(res.Variable.Variants))
	for _, sibling := range res.Variable.Variants {
		if sibling.ID == written.ID {
			continue
		}
		siblings = append(siblings, sibling.Attributes)
	}

	shadowerScopes := make([]string, 0)
	for _, probeAttrs := range shadowProbeContexts(written.Attributes, siblings) {
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

// shadowProbeContexts computes the distinct job contexts worth probing for a just-written variant: one
// per sibling scope that could apply to the same job. A sibling with no attributes is the catch-all
// default, which can never shadow another variant by specificity, so it is skipped; a sibling whose
// scope is disjoint from the written variant's can never apply to the same job, so it is skipped too.
// Each remaining probe context is the written variant's own scope widened with the dimensions the
// sibling constrains that the written variant leaves open. Results are de-duplicated and capped at
// maxShadowProbes so a bulk import can't fan out into an unbounded number of follow-up requests.
func shadowProbeContexts(written []api.CIVariantAttribute, siblings [][]api.CIVariantAttribute) [][]api.CIVariantAttribute {
	writtenByKey := map[string]map[string]bool{}
	for _, attr := range written {
		if writtenByKey[attr.Key] == nil {
			writtenByKey[attr.Key] = map[string]bool{}
		}
		writtenByKey[attr.Key][attr.Value] = true
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

// emitShadowWarning prints the stderr advisory pointing at the list command that reveals the shadowing
// winner. kind is the CLI noun ("secrets" or "vars") so the hint names the right command.
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

// scopesDisjoint reports whether a sibling's attributes contradict the written variant on any shared
// dimension, meaning no single job can match both. A dimension only contradicts when the two constrain
// it to entirely different value sets; attributes are repeatable, so a shared dimension with any
// overlapping value (an OR within the dimension) still lets both apply. Selector values may be globs
// (for example a branch of release/*), which an exact-string comparison would wrongly treat as a
// mismatch, so a pattern on either side keeps the dimension non-disjoint — the probe errs toward asking
// the server rather than silently skipping a sibling that could still match.
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
			if writtenValues[value] || looksLikePattern(value) {
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

// looksLikePattern reports whether an attribute value uses glob syntax, as branch and workflow
// selectors may. Such a value can match jobs an exact-string comparison would miss, so the disjoint
// check treats it as potentially overlapping.
func looksLikePattern(value string) bool {
	return strings.ContainsAny(value, "*?[]{}")
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

// variableShadowingWinner is the variable counterpart of shadowingWinner. Variables carry resolution
// only at the group level, so instead of a per-row lower-priority flag it reads the server's
// winner-first ordering: when the group resolved to a definite winner and the just-written variant is
// present among the candidates but is not that winner, it is shadowed and the winner is the top row.
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

// variableVariantRowResolution derives a per-row resolution string for a variable variant from the
// group-level resolution and the variant's position in the server's winner-first ordering. Variables
// do not carry per-variant resolution on the wire (unlike secrets), but the server orders variants
// winner-first and reports whether the group resolved to a definite winner, which is enough to label
// each row: when resolved, the first row wins and the rest are shadowed; when indeterminate, any row
// could still win with more context.
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
