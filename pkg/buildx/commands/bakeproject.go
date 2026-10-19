package commands

import (
	"bytes"
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
// upstream bake knows. So, before upstream bake reads the files, project_id is
// moved into an attribute that upstream bake knows, the carrier, and the
// references to project_id become references to the carrier. Upstream bake
// then evaluates the project with the same variables, functions,
// inheritance, and matrix expansion as every other attribute. Each read
// blanks the values of the carrier in the files, so that only projects reach
// it:
//
//   - projectFiles uses description as the carrier. The description has no
//     effect on a build, so these files are used for the build too.
//   - descriptionFiles keeps the descriptions of the user for bake --print,
//     and uses as the carrier an attribute that no expression in the files
//     reads. Only the descriptions of this read are used. When expressions
//     read every candidate attribute, it removes project_id instead, and
//     references to project_id read as an empty string.
//
// Compose services select a project with the x-depot.project-id build
// extension. Compose has no field that bake reads as a description, so
// projectFiles adds an HCL file that sets the description of each service to
// its project. Bake merges compose files before HCL files, so that HCL file
// comes first among the HCL files.
//
// When the files select no project, the only change is that each read of a
// whole target, such as lookup(target.base, "project_id", ""), gets an empty
// project_id, as with the earlier Depot bake schema. The descriptions stay
// available to expressions.
const projectIDAttribute = "project_id"

const buildCarrier = "description"

// descriptionCarriers are the attributes that descriptionFiles can use as the
// carrier, in order of preference.
var descriptionCarriers = []string{"call", "network", "target"}

const composeProjectFileName = "depot-compose-projects.hcl"

// readProjectTargets reads the targets and groups, and the project of each
// target that selects one.
func readProjectTargets(ctx context.Context, files []bake.File, composeTargets map[string]compose.Target, names, overrides []string, defaults map[string]string) (map[string]*bake.Target, map[string]*bake.Group, map[string]string, error) {
	if !selectsProjects(files, composeTargets) {
		targets, groups, err := bake.ReadTargets(ctx, rewriteFiles(files, ""), names, overrides, defaults, nil, &bake.EntitlementConf{})
		return targets, groups, map[string]string{}, err
	}
	targets, groups, err := bake.ReadTargets(ctx, projectFiles(files, composeTargets), names, overrides, defaults, nil, &bake.EntitlementConf{})
	if err != nil {
		return nil, nil, nil, err
	}
	return targets, groups, targetProjects(targets), nil
}

// selectsProjects reports whether a target sets project_id, an expression
// reads it, or a compose service sets x-depot.project-id.
func selectsProjects(files []bake.File, composeTargets map[string]compose.Target) bool {
	return slices.ContainsFunc(files, func(f bake.File) bool { _, uses := rewriteFile(f, buildCarrier); return uses > 0 }) ||
		slices.ContainsFunc(slices.Collect(maps.Values(composeTargets)), func(t compose.Target) bool { return t.ProjectID != "" })
}

// projectFiles returns the files with the project of each target in its
// description.
func projectFiles(files []bake.File, composeTargets map[string]compose.Target) []bake.File {
	out := rewriteFiles(files, buildCarrier)
	if f, ok := composeProjectFile(composeTargets); ok {
		out = append([]bake.File{f}, out...)
	}
	return out
}

// descriptionFiles returns the files with the descriptions of the user.
func descriptionFiles(files []bake.File) []bake.File {
	return rewriteFiles(files, descriptionCarrier(files))
}

// descriptionCarrier returns the first of descriptionCarriers that no
// expression in the files reads, such as with target.base.call. It returns
// an empty string, which removes project_id, when expressions read all of
// them, or read a whole target, such as with lookup(target.base, "call", "")
// or target.base[name], because the attributes such a read uses are not
// known before evaluation.
func descriptionCarrier(files []bake.File) string {
	if slices.ContainsFunc(files, func(f bake.File) bool { return readsTargets(f, targetObjectReads) }) {
		return ""
	}
	for _, carrier := range descriptionCarriers {
		attributeReads := func(node hclsyntax.Node) []*hclsyntax.ScopeTraversalExpr { return targetAttributeReads(node, carrier) }
		if !slices.ContainsFunc(files, func(f bake.File) bool { return readsTargets(f, attributeReads) }) {
			return carrier
		}
	}
	return ""
}

// readsTargets reports whether reads finds an expression in the file. Bake
// evaluates expressions only in HCL and JSON files, so it reports false for
// other files, such as compose files.
func readsTargets(f bake.File, reads func(hclsyntax.Node) []*hclsyntax.ScopeTraversalExpr) bool {
	if !strings.HasSuffix(f.Name, ".json") {
		if file, diags := hclsyntax.ParseConfig(f.Data, f.Name, hcl.InitialPos); !diags.HasErrors() {
			return len(reads(file.Body.(*hclsyntax.Body))) > 0
		}
	}
	root, err := parseJSON(f.Data)
	if err != nil {
		return false
	}
	found := false
	root.visitTemplates(func(s *jsonValue) {
		if expr, diags := hclsyntax.ParseTemplate([]byte(s.text), "", hcl.InitialPos); !diags.HasErrors() {
			found = found || len(reads(expr)) > 0
		}
	})
	return found
}

// targetProjects returns the project of each target that has one, and
// clears the descriptions that carried them.
func targetProjects(targets map[string]*bake.Target) map[string]string {
	projects := map[string]string{}
	for name, t := range targets {
		if t.Description != "" {
			projects[name] = t.Description
		}
		t.Description = ""
	}
	return projects
}

// readTargetDescriptions sets the description of each target to the value
// in the files, for bake --print. readProjectTargets clears the descriptions
// when the files select projects.
func readTargetDescriptions(ctx context.Context, files []bake.File, composeTargets map[string]compose.Target, targets map[string]*bake.Target, names, overrides []string, defaults map[string]string) error {
	if !selectsProjects(files, composeTargets) || !slices.ContainsFunc(files, func(f bake.File) bool { return bytes.Contains(f.Data, []byte("description")) }) {
		return nil
	}
	resolved, _, err := bake.ReadTargets(ctx, descriptionFiles(files), names, overrides, defaults, nil, &bake.EntitlementConf{})
	if err != nil {
		return err
	}
	for name, t := range targets {
		if r, ok := resolved[name]; ok {
			t.Description = r.Description
		}
	}
	return nil
}

// composeProjectFile returns an HCL file that sets the description of each
// compose service with an x-depot.project-id to that project. It returns
// false when no compose service selects a project.
func composeProjectFile(composeTargets map[string]compose.Target) (bake.File, bool) {
	f := hclwrite.NewEmptyFile()
	found := false
	for _, name := range slices.Sorted(maps.Keys(composeTargets)) {
		projectID := composeTargets[name].ProjectID
		if projectID == "" {
			continue
		}
		f.Body().AppendNewBlock("target", []string{name}).Body().SetAttributeValue(buildCarrier, cty.StringVal(projectID))
		found = true
	}
	return bake.File{Name: composeProjectFileName, Data: f.Bytes()}, found
}

func rewriteFiles(files []bake.File, carrier string) []bake.File {
	out := make([]bake.File, len(files))
	for i, f := range files {
		data, _ := rewriteFile(f, carrier)
		out[i] = bake.File{Name: f.Name, Data: data}
	}
	return out
}

// rewriteFile moves project_id into the carrier. It returns the file and the
// number of project_id attributes and references that it moved. A file that
// is neither HCL nor JSON is returned unchanged.
func rewriteFile(f bake.File, carrier string) ([]byte, int) {
	if strings.HasSuffix(f.Name, ".json") {
		if data, uses, ok := rewriteJSON(f.Data, carrier); ok {
			return data, uses
		}
		return f.Data, 0
	}
	if data, uses, ok := rewriteHCL(f.Data, f.Name, carrier); ok {
		return data, uses
	}
	if data, uses, ok := rewriteJSON(f.Data, carrier); ok {
		return data, uses
	}
	return f.Data, 0
}

type replacement struct {
	start, end int
	text       []byte
}

func rewriteHCL(data []byte, name, carrier string) ([]byte, int, bool) {
	file, diags := hclsyntax.ParseConfig(data, name, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, 0, false
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return nil, 0, false
	}

	var blanks, renames []replacement
	uses := 0
	for _, block := range body.Blocks {
		if block.Type != "target" {
			continue
		}
		if attr, ok := block.Body.Attributes[carrier]; ok && carrier != "" {
			blanks = append(blanks, blank(data, attr.SrcRange))
		}
		if attr, ok := block.Body.Attributes[projectIDAttribute]; ok {
			uses++
			if carrier == "" {
				blanks = append(blanks, blank(data, attr.SrcRange))
			} else {
				renames = append(renames, replacement{start: attr.NameRange.Start.Byte, end: attr.NameRange.End.Byte, text: []byte(carrier)})
			}
		}
	}
	references, reads := projectIDReferences(body, carrier)
	uses += reads
	renames = append(renames, references...)

	replacements := blanks
	for _, r := range renames {
		inBlank := slices.ContainsFunc(blanks, func(b replacement) bool { return r.start >= b.start && r.end <= b.end })
		if !inBlank {
			replacements = append(replacements, r)
		}
	}
	return replace(data, replacements), uses, true
}

// projectIDReferences returns the replacements that change the expressions
// in node that read the project_id of a target, such as
// target.base.project_id or target.base["project_id"], into reads of the
// carrier, and the number of these expressions. A target without project_id
// reads as an empty string, as with the earlier Depot bake schema. Each read
// of a whole target, such as lookup(target.base, "project_id", ""), gets
// project_id too.
func projectIDReferences(node hclsyntax.Node, carrier string) ([]replacement, int) {
	var out []replacement
	reads := targetAttributeReads(node, projectIDAttribute)
	for _, expr := range reads {
		if carrier == "" {
			out = append(out, replacement{start: expr.SrcRange.Start.Byte, end: expr.SrcRange.End.Byte, text: []byte(`""`)})
			continue
		}
		if name, ok := expr.Traversal[1].(hcl.TraverseAttr); ok && len(expr.Traversal) == 3 {
			out = append(out, replacement{start: expr.SrcRange.Start.Byte, end: expr.SrcRange.End.Byte, text: []byte(carrierRead(name.Name, carrier))})
			continue
		}
		step := expr.Traversal[2].SourceRange()
		out = append(out, replacement{start: step.Start.Byte, end: step.End.Byte, text: []byte(`["` + carrier + `"]`)})
	}
	for _, expr := range targetObjectReads(node) {
		name := expr.Traversal[1].(hcl.TraverseAttr).Name
		projectID := `""`
		if carrier != "" {
			projectID = carrierRead(name, carrier)
		}
		out = append(out, replacement{start: expr.SrcRange.Start.Byte, end: expr.SrcRange.End.Byte, text: []byte(`merge(target.` + name + `, { ` + projectIDAttribute + ` = ` + projectID + ` })`)})
	}
	return out, len(reads)
}

func carrierRead(target, carrier string) string {
	ref := "target." + target + "." + carrier
	return `(` + ref + ` == null ? "" : ` + ref + `)`
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
	slices.SortFunc(replacements, func(a, b replacement) int { return a.start - b.start })
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

func rewriteTemplate(s, carrier string) (string, int) {
	if !strings.Contains(s, "target") {
		return s, 0
	}
	expr, diags := hclsyntax.ParseTemplate([]byte(s), "", hcl.InitialPos)
	if diags.HasErrors() {
		return s, 0
	}
	replacements, reads := projectIDReferences(expr, carrier)
	return string(replace([]byte(s), replacements)), reads
}
