package commands

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/depot/cli/pkg/compose"
	"github.com/docker/buildx/bake"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// Depot extends bake targets with a project_id attribute that selects the
// Depot project of the target. Upstream bake rejects unknown attributes, and
// a reference such as target.base.project_id can only read attributes that
// upstream bake knows. So, before upstream bake reads the files, each
// project_id moves into the projectLabel key of the labels of its target,
// and each reference to project_id becomes a read of that key. Upstream bake
// then evaluates the project with the same variables, functions,
// inheritance, and matrix expansion as every other attribute, and merges
// labels one key at a time, so that a target inherits the project of its
// parent. An empty project_id adds no key, so that it does not replace an
// inherited project. After the read, targetProjects removes the key from
// the labels.
//
// A target that sets project_id but no labels gets the unsetLabelsLabel key
// too. Reads of the labels of a target, such as
// merge(target.base.labels, {}), and reads of a whole target, such as
// lookup(target.base, "labels", {}), get the labels without both keys, or
// null when only these keys are present, so that they read the same labels
// as with the earlier Depot bake schema.
//
// Compose services select a project with the x-depot.project-id build
// extension. projectFiles adds an HCL file that sets the label for each such
// service. Bake merges compose files before HCL files, so that HCL file
// comes first among the HCL files.
//
// When the files select no project, the only change is that each read of a
// whole target gets an empty project_id, as with the earlier Depot bake
// schema.
const projectIDAttribute = "project_id"

const projectLabel = "__depot_project_id"

const unsetLabelsLabel = "__depot_labels_unset"

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
// labels.
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
// removes the projects from the labels.
func targetProjects(targets map[string]*bake.Target) map[string]string {
	projects := map[string]string{}
	for name, t := range targets {
		if projectID := t.Labels[projectLabel]; projectID != nil && *projectID != "" {
			projects[name] = *projectID
		}
		delete(t.Labels, projectLabel)
		delete(t.Labels, unsetLabelsLabel)
		if len(t.Labels) == 0 {
			t.Labels = nil
		}
	}
	return projects
}

// composeProjectFile returns an HCL file that sets the project label of each
// compose service with an x-depot.project-id. It returns
// false when no compose service selects a project.
func composeProjectFile(composeTargets map[string]compose.Target) (bake.File, bool) {
	f := hclwrite.NewEmptyFile()
	found := false
	for _, name := range slices.Sorted(maps.Keys(composeTargets)) {
		projectID := composeTargets[name].ProjectID
		if projectID == "" {
			continue
		}
		f.Body().AppendNewBlock("target", []string{name}).Body().SetAttributeValue("labels", cty.MapVal(map[string]cty.Value{
			unsetLabelsLabel: cty.StringVal("true"),
			projectLabel:     cty.StringVal(projectID),
		}))
		found = true
	}
	return bake.File{Name: composeProjectFileName, Data: f.Bytes()}, found
}

// rewriteFile moves project_id into the labels when the files select
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

	reads, uses := projectReads(body, selects, nil)
	var moves []replacement
	for _, block := range body.Blocks {
		attr, ok := block.Body.Attributes[projectIDAttribute]
		if block.Type != "target" || !ok {
			continue
		}
		uses++
		value := attr.Expr.Range()
		projectText := rewrittenText(data, reads, value.Start.Byte, value.End.Byte)
		project := projectText + heredocEnd(projectText)
		if self := selfProject(block, project); self != nil {
			blockReads, _ := projectReads(block.Body, selects, self)
			reads = slices.Concat(outside(reads, blockReads), blockReads)
		}
		labels, ok := block.Body.Attributes["labels"]
		if ok && !isNull(labels.Expr) {
			moves = append(moves, blank(data, attr.SrcRange), replacement{start: labels.Expr.Range().Start.Byte, end: labels.Expr.Range().End.Byte, text: []byte(
				`merge(` + expressionText(data, reads, labels.Expr) + `, ` + projectLabels(project) + `)`)})
			continue
		}
		if ok {
			moves = append(moves, blank(data, labels.SrcRange))
		}
		moves = append(moves,
			replacement{start: attr.NameRange.Start.Byte, end: attr.NameRange.End.Byte, text: []byte("labels")},
			replacement{start: value.Start.Byte, end: value.Start.Byte, text: []byte(`merge({ "` + unsetLabelsLabel + `" = "true" }, { for project in compact([`)},
			replacement{start: value.End.Byte, end: value.End.Byte, text: []byte(heredocEnd(projectText) + `]) : "` + projectLabel + `" => project })`)})
	}
	return replace(data, slices.Concat(moves, outside(reads, moves))), uses, true
}

// selfProject returns the project of a target block for the reads of its own
// project inside the block, which would otherwise read its labels from
// within its labels. It returns nil for a matrix target, whose name is not
// its label.
func selfProject(block *hclsyntax.Block, project string) *targetProject {
	if _, ok := block.Body.Attributes["matrix"]; ok || len(block.Labels) != 1 {
		return nil
	}
	return &targetProject{target: block.Labels[0], expression: project}
}

type targetProject struct {
	target, expression string
}

func projectLabels(project string) string {
	return `{ for project in compact([` + project + `]) : "` + projectLabel + `" => project }`
}

func isNull(expr hclsyntax.Expression) bool {
	literal, ok := expr.(*hclsyntax.LiteralValueExpr)
	return ok && literal.Val.IsNull()
}

// expressionText returns the source text of an expression with the
// replacements in its range. Text that ends with a heredoc ends with a line
// break, so that more text can follow it.
func expressionText(data []byte, replacements []replacement, expr hclsyntax.Expression) string {
	text := rewrittenText(data, replacements, expr.Range().Start.Byte, expr.Range().End.Byte)
	return text + heredocEnd(text)
}

func heredocEnd(text string) string {
	tokens, _ := hclsyntax.LexExpression([]byte(text+"\n"), "", hcl.InitialPos)
	tokens = slices.DeleteFunc(tokens, func(t hclsyntax.Token) bool { return t.Type == hclsyntax.TokenEOF || t.Type == hclsyntax.TokenNewline })
	if len(tokens) > 0 && tokens[len(tokens)-1].Type == hclsyntax.TokenCHeredoc && !strings.HasSuffix(text, "\n") {
		return "\n"
	}
	return ""
}

// projectReads returns the replacements that change the expressions in node
// that read a target, and the number of reads of project_id among them:
//
//   - A read of project_id, such as target.base.project_id or
//     target.base["project_id"], reads the project label, or an empty string
//     when the target has no project, as with the earlier Depot bake schema.
//     A read of the project of self, the target that contains node, reads
//     its project expression.
//   - A read of labels, such as target.base.labels, reads the labels without
//     the project label.
//   - A read of a whole target, such as lookup(target.base, "project_id", ""),
//     reads the target with project_id and with the labels without the
//     project label.
//
// When the files select no project, only a read of project_id or a whole
// target changes, and project_id is an empty string.
func projectReads(node hclsyntax.Node, selects bool, self *targetProject) ([]replacement, int) {
	var out []replacement
	projectIDReads := targetAttributeReads(node, projectIDAttribute)
	for _, expr := range projectIDReads {
		name, ok := expr.Traversal[1].(hcl.TraverseAttr)
		if !ok {
			continue
		}
		read := projectRead(name.Name, selects)
		if self != nil && name.Name == self.target {
			read = `coalesce(` + self.expression + `, "")`
		}
		out = append(out, replacement{start: expr.SrcRange.Start.Byte, end: expr.Traversal[2].SourceRange().End.Byte, text: []byte(read)})
	}
	if selects {
		for _, expr := range targetAttributeReads(node, "labels") {
			if name, ok := expr.Traversal[1].(hcl.TraverseAttr); ok {
				out = append(out, replacement{start: expr.SrcRange.Start.Byte, end: expr.Traversal[2].SourceRange().End.Byte, text: []byte(labelsRead(name.Name))})
			}
		}
	}
	for _, expr := range targetObjectReads(node) {
		name := expr.Traversal[1].(hcl.TraverseAttr).Name
		fields := projectIDAttribute + ` = ` + projectRead(name, selects)
		if selects {
			fields += `, labels = ` + labelsRead(name)
		}
		out = append(out, replacement{start: expr.SrcRange.Start.Byte, end: expr.SrcRange.End.Byte, text: []byte(`merge(target.` + name + `, { ` + fields + ` })`)})
	}
	return out, len(projectIDReads)
}

func projectRead(target string, selects bool) string {
	if !selects {
		return `""`
	}
	return `coalesce(lookup(merge({}, target.` + target + `.labels), "` + projectLabel + `", ""), "")`
}

func labelsRead(target string) string {
	labels := "target." + target + ".labels"
	userLabel := `key != "` + projectLabel + `" && key != "` + unsetLabelsLabel + `"`
	unset := `length([for key in keys(merge({}, ` + labels + `)) : key if ` + userLabel + `]) == 0 && contains(keys(merge({}, ` + labels + `)), "` + unsetLabelsLabel + `")`
	return `(` + labels + ` == null || ` + unset + ` ? null : convert({ for key, value in ` + labels + ` : key => value if ` + userLabel + ` }, map(string)))`
}

// rewrittenText returns the source text between start and end with the
// replacements in that range.
func rewrittenText(data []byte, replacements []replacement, start, end int) string {
	var inner []replacement
	for _, r := range replacements {
		if r.start >= start && r.end <= end {
			inner = append(inner, replacement{start: r.start - start, end: r.end - start, text: r.text})
		}
	}
	return string(replace(data[start:end], inner))
}

// outside returns the replacements that are not inside one of the ranges.
func outside(replacements, ranges []replacement) []replacement {
	return slices.DeleteFunc(slices.Clone(replacements), func(r replacement) bool {
		return slices.ContainsFunc(ranges, func(rng replacement) bool { return rng.start < rng.end && r.start >= rng.start && r.end <= rng.end })
	})
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

// blank replaces a range with spaces and keeps line breaks, so that the
// positions in error messages for the rest of the file do not change.
func blank(data []byte, rng hcl.Range) replacement {
	return blankBytes(data, rng.Start.Byte, rng.End.Byte)
}

func blankBytes(data []byte, start, end int) replacement {
	text := slices.Clone(data[start:end])
	for i, c := range text {
		if c != '\n' && c != '\r' {
			text[i] = ' '
		}
	}
	return replacement{start: start, end: end, text: text}
}

func rewriteTemplate(s string, selects bool, self *targetProject) (string, int) {
	if !strings.Contains(s, "target") {
		return s, 0
	}
	expr, diags := hclsyntax.ParseTemplate([]byte(s), "", hcl.InitialPos)
	if diags.HasErrors() {
		return s, 0
	}
	replacements, reads := projectReads(expr, selects, self)
	return string(replace([]byte(s), replacements)), reads
}
