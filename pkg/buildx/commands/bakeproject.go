package commands

import (
	"context"
	"crypto/rand"
	"maps"
	"slices"
	"strings"

	"github.com/depot/cli/pkg/compose"
	"github.com/docker/buildx/bake"
	"github.com/docker/buildx/util/buildflags"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// Depot extends bake targets with a project_id attribute that selects the
// Depot project of the target. Upstream bake rejects unknown attributes, and
// a reference such as target.base.project_id can only read attributes that
// upstream bake knows. So, before upstream bake reads the files, each
// project_id becomes a policy entry whose filename is projectPolicyPrefix
// followed by the project, and each reference to project_id becomes a read
// of that entry. Upstream bake then evaluates the project with the same
// variables, functions, inheritance, and matrix expansion as every other
// attribute. Depot builders do not support source policies, so no user
// value or expression uses the policy attribute.
//
// Bake appends the policy entries of a parent before those of its child,
// and those of earlier files before those of later files. So the last entry
// with a project selects the project, in the same way as the last value of
// an attribute. An entry with an empty project selects no project, and does
// not replace an earlier project. After the read, targetProjects removes the
// entries.
//
// Compose services select a project with the x-depot.project-id build
// extension. projectFiles adds an HCL file that sets the policy entry for
// each such service. Bake merges compose files before HCL files, so that HCL
// file comes first among the HCL files.
//
// When the files select no project, the only change is that each read of a
// whole target gets an empty project_id, as with the earlier Depot bake
// schema.
const projectIDAttribute = "project_id"

// projectPolicyPrefix starts the filename of each policy entry that carries
// a project. It contains a random value, so that no policy of the user
// starts with it.
var projectPolicyPrefix = "depot-project-" + rand.Text() + ":"

const composeProjectFileName = "depot-compose-projects.hcl"

// readProjectTargets reads the targets and groups, and the project of each
// target that selects one.
func readProjectTargets(ctx context.Context, files []bake.File, composeTargets map[string]compose.Target, names, overrides []string, defaults map[string]string) (map[string]*bake.Target, map[string]*bake.Group, map[string]string, error) {
	selects := selectsProjects(files, composeTargets)
	targets, groups, err := bake.ReadTargets(ctx, projectFiles(files, composeTargets, selects), names, overrides, defaults, nil, &bake.EntitlementConf{})
	if err != nil {
		return nil, nil, nil, err
	}
	if !selects {
		return targets, groups, map[string]string{}, nil
	}
	return targets, groups, targetProjects(targets), nil
}

// selectsProjects reports whether a target sets project_id, an expression
// reads it, or a compose service sets x-depot.project-id.
func selectsProjects(files []bake.File, composeTargets map[string]compose.Target) bool {
	return slices.ContainsFunc(files, func(f bake.File) bool { _, uses := rewriteFile(f, true); return uses > 0 }) ||
		slices.ContainsFunc(slices.Collect(maps.Values(composeTargets)), func(t compose.Target) bool { return t.ProjectID != "" })
}

// projectFiles returns the files with the project of each target in its
// policy.
func projectFiles(files []bake.File, composeTargets map[string]compose.Target, selects bool) []bake.File {
	out := make([]bake.File, len(files))
	for i, f := range files {
		data, _ := rewriteFile(f, selects)
		out[i] = bake.File{Name: f.Name, Data: data}
	}
	if f, ok := composeProjectFile(composeTargets); ok {
		out = append([]bake.File{f}, out...)
	}
	return out
}

// targetProjects returns the project of each target that has one, and
// removes the policy entries that carried the projects.
func targetProjects(targets map[string]*bake.Target) map[string]string {
	projects := map[string]string{}
	for name, t := range targets {
		t.Policy = slices.DeleteFunc(t.Policy, func(p buildflags.PolicyConfig) bool {
			if len(p.Files) != 1 || !strings.HasPrefix(p.Files[0].Filename, projectPolicyPrefix) {
				return false
			}
			if projectID := strings.TrimPrefix(p.Files[0].Filename, projectPolicyPrefix); projectID != "" {
				projects[name] = projectID
			}
			return true
		})
		if len(t.Policy) == 0 {
			t.Policy = nil
		}
	}
	return projects
}

// composeProjectFile returns an HCL file that sets the project of each
// compose service with an x-depot.project-id. It returns false when no
// compose service selects a project.
func composeProjectFile(composeTargets map[string]compose.Target) (bake.File, bool) {
	f := hclwrite.NewEmptyFile()
	found := false
	for _, name := range slices.Sorted(maps.Keys(composeTargets)) {
		projectID := composeTargets[name].ProjectID
		if projectID == "" {
			continue
		}
		entry := cty.MapVal(map[string]cty.Value{"filename": cty.StringVal(projectPolicyPrefix + projectID)})
		f.Body().AppendNewBlock("target", []string{name}).Body().SetAttributeValue("policy", cty.ListVal([]cty.Value{entry}))
		found = true
	}
	return bake.File{Name: composeProjectFileName, Data: f.Bytes()}, found
}

// rewriteFile moves project_id into the policy when the files select
// projects. It returns the file and the number of project_id attributes and
// references. A file that is neither HCL nor JSON is returned unchanged.
func rewriteFile(f bake.File, selects bool) ([]byte, int) {
	if strings.HasSuffix(f.Name, ".json") {
		if data, uses, ok := rewriteJSON(f.Data, selects); ok {
			return data, uses
		}
		return f.Data, 0
	}
	if data, uses, ok := rewriteHCL(f.Data, f.Name, selects); ok {
		return data, uses
	}
	if data, uses, ok := rewriteJSON(f.Data, selects); ok {
		return data, uses
	}
	return f.Data, 0
}

type replacement struct {
	start, end int
	text       []byte
}

func rewriteHCL(data []byte, name string, selects bool) ([]byte, int, bool) {
	file, diags := hclsyntax.ParseConfig(data, name, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, 0, false
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return nil, 0, false
	}

	replacements, uses := projectReads(body, selects)
	for _, block := range body.Blocks {
		attr, ok := block.Body.Attributes[projectIDAttribute]
		if block.Type != "target" || !ok {
			continue
		}
		uses++
		value := attr.Expr.Range()
		replacements = append(replacements,
			replacement{start: attr.NameRange.Start.Byte, end: attr.NameRange.End.Byte, text: []byte("policy")},
			replacement{start: value.Start.Byte, end: value.Start.Byte, text: []byte(`[{ filename = "` + projectPolicyPrefix + `${`)},
			replacement{start: value.End.Byte, end: value.End.Byte, text: []byte(heredocEnd(string(data[value.Start.Byte:value.End.Byte])) + `}" }]`)})
	}
	return replace(data, replacements), uses, true
}

// heredocEnd returns a line break when the expression ends with a heredoc,
// so that more text can follow it.
func heredocEnd(expr string) string {
	tokens, _ := hclsyntax.LexExpression([]byte(expr+"\n"), "", hcl.InitialPos)
	tokens = slices.DeleteFunc(tokens, func(t hclsyntax.Token) bool { return t.Type == hclsyntax.TokenEOF || t.Type == hclsyntax.TokenNewline })
	if len(tokens) > 0 && tokens[len(tokens)-1].Type == hclsyntax.TokenCHeredoc {
		return "\n"
	}
	return ""
}

// projectReads returns the replacements that change the expressions in node
// that read the project of a target, and the number of reads of project_id
// among them:
//
//   - A read of project_id, such as target.base.project_id or
//     target.base["project_id"], reads the project of the last policy entry
//     with a project, or an empty string when the target has no project, as
//     with the earlier Depot bake schema.
//   - A read of policy, such as target.base.policy, reads the entries
//     without those that carry projects.
//   - A read of a whole target, such as lookup(target.base, "project_id", ""),
//     reads the target with project_id and without the policy entries that
//     carry projects.
//
// When the files select no project, project_id is an empty string.
func projectReads(node hclsyntax.Node, selects bool) ([]replacement, int) {
	var out []replacement
	projectIDReads := targetAttributeReads(node, projectIDAttribute)
	for _, expr := range projectIDReads {
		if name, ok := expr.Traversal[1].(hcl.TraverseAttr); ok {
			out = append(out, replacement{start: expr.SrcRange.Start.Byte, end: expr.Traversal[2].SourceRange().End.Byte, text: []byte(projectRead(name.Name, selects))})
		}
	}
	if selects {
		for _, expr := range targetAttributeReads(node, "policy") {
			if name, ok := expr.Traversal[1].(hcl.TraverseAttr); ok {
				out = append(out, replacement{start: expr.SrcRange.Start.Byte, end: expr.Traversal[2].SourceRange().End.Byte, text: []byte("(" + userPolicies(name.Name) + ")")})
			}
		}
	}
	for _, expr := range targetObjectReads(node) {
		name := expr.Traversal[1].(hcl.TraverseAttr).Name
		fields := projectIDAttribute + ` = ` + projectRead(name, selects)
		if selects {
			fields += `, policy = ` + userPolicies(name)
		}
		out = append(out, replacement{start: expr.SrcRange.Start.Byte, end: expr.SrcRange.End.Byte, text: []byte(`merge(target.` + name + `, { ` + fields + ` })`)})
	}
	return out, len(projectIDReads)
}

func projectRead(target string, selects bool) string {
	if !selects {
		return `""`
	}
	return `reverselist(concat([""], [for policy in target.` + target + `.policy : trimprefix(` + policyFilename + `, "` + projectPolicyPrefix + `") if length(regexall("(?s)^` + projectPolicyPrefix + `.", ` + policyFilename + `)) > 0]))[0]`
}

// userPolicies returns an expression that reads the policy entries of a
// target without those that carry its project.
func userPolicies(target string) string {
	return `[for policy in target.` + target + `.policy : policy if length(regexall("^` + projectPolicyPrefix + `", ` + policyFilename + `)) == 0]`
}

const policyFilename = `lookup(policy, "filename", "")`

func rewriteTemplate(s string, selects bool) (string, int) {
	if !strings.Contains(s, "target") {
		return s, 0
	}
	expr, diags := hclsyntax.ParseTemplate([]byte(s), "", hcl.InitialPos)
	if diags.HasErrors() {
		return s, 0
	}
	replacements, reads := projectReads(expr, selects)
	return string(replace([]byte(s), replacements)), reads
}

// targetAttributeReads returns the expressions in node that read an
// attribute of a target.
func targetAttributeReads(node hclsyntax.Node, attribute string) []*hclsyntax.ScopeTraversalExpr {
	return slices.DeleteFunc(targetReads(node), func(expr *hclsyntax.ScopeTraversalExpr) bool {
		return len(expr.Traversal) < 3 || !traversesTo(expr.Traversal[2], attribute)
	})
}

// targetObjectReads returns the expressions in node that read a whole
// target, such as target.base.
func targetObjectReads(node hclsyntax.Node) []*hclsyntax.ScopeTraversalExpr {
	return slices.DeleteFunc(targetReads(node), func(expr *hclsyntax.ScopeTraversalExpr) bool {
		_, ok := expr.Traversal[len(expr.Traversal)-1].(hcl.TraverseAttr)
		return len(expr.Traversal) != 2 || !ok
	})
}

// targetReads returns the expressions in node that read a target. In a for
// expression, a function, or a matrix target with a variable named target,
// that name refers to the variable.
func targetReads(node hclsyntax.Node) []*hclsyntax.ScopeTraversalExpr {
	var reads []*hclsyntax.ScopeTraversalExpr
	var scopes []hcl.Range
	_ = hclsyntax.VisitAll(node, func(node hclsyntax.Node) hcl.Diagnostics {
		switch node := node.(type) {
		case *hclsyntax.ScopeTraversalExpr:
			if node.Traversal.RootName() == "target" {
				reads = append(reads, node)
			}
		case *hclsyntax.ForExpr:
			if node.KeyVar == "target" || node.ValVar == "target" {
				for _, expr := range []hclsyntax.Expression{node.KeyExpr, node.ValExpr, node.CondExpr} {
					if expr != nil {
						scopes = append(scopes, expr.Range())
					}
				}
			}
		case *hclsyntax.Block:
			if result, ok := node.Body.Attributes["result"]; ok && node.Type == "function" && hasTargetParameter(node.Body) {
				scopes = append(scopes, result.Expr.Range())
			}
			if matrix, ok := node.Body.Attributes["matrix"]; ok && node.Type == "target" && hasTargetVariable(matrix.Expr) {
				for name, attr := range node.Body.Attributes {
					if name != "matrix" {
						scopes = append(scopes, attr.Expr.Range())
					}
				}
			}
		}
		return nil
	})
	return slices.DeleteFunc(reads, func(expr *hclsyntax.ScopeTraversalExpr) bool {
		return slices.ContainsFunc(scopes, func(scope hcl.Range) bool { return scope.ContainsOffset(expr.SrcRange.Start.Byte) })
	})
}

func hasTargetParameter(body *hclsyntax.Body) bool {
	if attr, ok := body.Attributes["variadic_params"]; ok && hcl.ExprAsKeyword(attr.Expr) == "target" {
		return true
	}
	attr, ok := body.Attributes["params"]
	if !ok {
		return false
	}
	params, _ := hcl.ExprList(attr.Expr)
	return slices.ContainsFunc(params, func(param hcl.Expression) bool { return hcl.ExprAsKeyword(param) == "target" })
}

func hasTargetVariable(matrix hclsyntax.Expression) bool {
	object, ok := matrix.(*hclsyntax.ObjectConsExpr)
	return ok && slices.ContainsFunc(object.Items, func(item hclsyntax.ObjectConsItem) bool {
		if hcl.ExprAsKeyword(item.KeyExpr) == "target" {
			return true
		}
		key, diags := item.KeyExpr.Value(nil)
		return !diags.HasErrors() && key.Type() == cty.String && key.IsKnown() && !key.IsNull() && key.AsString() == "target"
	})
}

func traversesTo(step hcl.Traverser, attribute string) bool {
	switch step := step.(type) {
	case hcl.TraverseAttr:
		return step.Name == attribute
	case hcl.TraverseIndex:
		return step.Key.Type() == cty.String && step.Key.IsKnown() && !step.Key.IsNull() && step.Key.AsString() == attribute
	}
	return false
}

func replace(data []byte, replacements []replacement) []byte {
	slices.SortFunc(replacements, func(a, b replacement) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return a.end - b.end
	})
	out := make([]byte, 0, len(data))
	end := 0
	for _, r := range replacements {
		out = append(append(out, data[end:r.start]...), r.text...)
		end = r.end
	}
	return append(out, data[end:]...)
}
