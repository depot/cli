package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
//     reads. Only the descriptions of this read are used.
//
// Compose services select a project with the x-depot.project-id build
// extension. Compose has no field that bake reads as a description, so
// projectFiles adds an HCL file that sets the description of each service to
// its project. Bake merges compose files before HCL files, so that HCL file
// comes first among the HCL files.
//
// Files that select no project are read without changes, so that their
// descriptions stay available to expressions.
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
		targets, groups, err := bake.ReadTargets(ctx, files, names, overrides, defaults, nil, &bake.EntitlementConf{})
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
// expression in the files reads, such as with target.base.call.
func descriptionCarrier(files []bake.File) string {
	for _, carrier := range descriptionCarriers {
		if !slices.ContainsFunc(files, func(f bake.File) bool { return readsTargetAttribute(f, carrier) }) {
			return carrier
		}
	}
	return descriptionCarriers[0]
}

// readsTargetAttribute reports whether an expression in the file reads an
// attribute of a target. It reports true for a file that it cannot parse
// and that contains the name of the attribute.
func readsTargetAttribute(f bake.File, attribute string) bool {
	if !strings.HasSuffix(f.Name, ".json") {
		if file, diags := hclsyntax.ParseConfig(f.Data, f.Name, hcl.InitialPos); !diags.HasErrors() {
			return len(targetAttributeReads(file.Body.(*hclsyntax.Body), attribute)) > 0
		}
	}
	var root any
	if err := json.Unmarshal(f.Data, &root); err != nil {
		return bytes.Contains(f.Data, []byte(attribute))
	}
	reads := false
	visitJSONStrings(root, func(s string) {
		if expr, diags := hclsyntax.ParseTemplate([]byte(s), "", hcl.InitialPos); !diags.HasErrors() {
			reads = reads || len(targetAttributeReads(expr, attribute)) > 0
		}
	})
	return reads
}

func visitJSONStrings(value any, visit func(string)) {
	switch v := value.(type) {
	case string:
		visit(v)
	case map[string]any:
		for _, element := range v {
			visitJSONStrings(element, visit)
		}
	case []any:
		for _, element := range v {
			visitJSONStrings(element, visit)
		}
	}
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
	for _, block := range body.Blocks {
		if block.Type != "target" {
			continue
		}
		if attr, ok := block.Body.Attributes[carrier]; ok {
			blanks = append(blanks, blank(data, attr.SrcRange))
		}
		if attr, ok := block.Body.Attributes[projectIDAttribute]; ok {
			renames = append(renames, replacement{start: attr.NameRange.Start.Byte, end: attr.NameRange.End.Byte, text: []byte(carrier)})
		}
	}
	renames = append(renames, projectIDReferences(body, carrier)...)

	replacements := blanks
	for _, r := range renames {
		inBlank := slices.ContainsFunc(blanks, func(b replacement) bool { return r.start >= b.start && r.end <= b.end })
		if !inBlank {
			replacements = append(replacements, r)
		}
	}
	return replace(data, replacements), len(renames), true
}

// projectIDReferences returns the replacements that change the expressions
// in node that read the project_id of a target, such as
// target.base.project_id or target.base["project_id"], into reads of the
// carrier. A target without project_id reads as an empty string, as with
// the earlier Depot bake schema.
func projectIDReferences(node hclsyntax.Node, carrier string) []replacement {
	var out []replacement
	for _, expr := range targetAttributeReads(node, projectIDAttribute) {
		if name, ok := expr.Traversal[1].(hcl.TraverseAttr); ok && len(expr.Traversal) == 3 {
			ref := "target." + name.Name + "." + carrier
			out = append(out, replacement{start: expr.SrcRange.Start.Byte, end: expr.SrcRange.End.Byte, text: []byte(`(` + ref + ` == null ? "" : ` + ref + `)`)})
			continue
		}
		step := expr.Traversal[2].SourceRange()
		out = append(out, replacement{start: step.Start.Byte, end: step.End.Byte, text: []byte(`["` + carrier + `"]`)})
	}
	return out
}

// targetAttributeReads returns the expressions in node that read an
// attribute of a target.
func targetAttributeReads(node hclsyntax.Node, attribute string) []*hclsyntax.ScopeTraversalExpr {
	var out []*hclsyntax.ScopeTraversalExpr
	_ = hclsyntax.VisitAll(node, func(node hclsyntax.Node) hcl.Diagnostics {
		expr, ok := node.(*hclsyntax.ScopeTraversalExpr)
		if ok && expr.Traversal.RootName() == "target" && len(expr.Traversal) >= 3 && traversesTo(expr.Traversal[2], attribute) {
			out = append(out, expr)
		}
		return nil
	})
	return out
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
	slices.SortFunc(replacements, func(a, b replacement) int { return b.start - a.start })
	out := slices.Clone(data)
	for _, r := range replacements {
		out = slices.Concat(out[:r.start], r.text, out[r.end:])
	}
	return out
}

// blank replaces a range with spaces and keeps line breaks, so that the
// positions in error messages for the rest of the file do not change.
func blank(data []byte, rng hcl.Range) replacement {
	text := slices.Clone(data[rng.Start.Byte:rng.End.Byte])
	for i, c := range text {
		if c != '\n' && c != '\r' {
			text[i] = ' '
		}
	}
	return replacement{start: rng.Start.Byte, end: rng.End.Byte, text: text}
}

// rewriteJSON rewrites a JSON definition. It returns the data unchanged when
// nothing is rewritten, so that the positions in error messages stay correct.
func rewriteJSON(data []byte, carrier string) ([]byte, int, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, 0, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, 0, false
	}

	changed := false
	uses := 0
	targets, _ := root["target"].(map[string]any)
	for _, value := range targets {
		target, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := target[carrier]; ok {
			delete(target, carrier)
			changed = true
		}
		if projectID, ok := target[projectIDAttribute]; ok {
			delete(target, projectIDAttribute)
			target[carrier] = projectID
			changed = true
			uses++
		}
	}
	uses += rewriteJSONTemplates(root, carrier)
	if !changed && uses == 0 {
		return data, 0, true
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, 0, false
	}
	return out, uses, true
}

// rewriteJSONTemplates rewrites the references to project_id in every
// string of a decoded JSON value, and returns their number. Bake reads each
// string as a template.
func rewriteJSONTemplates(value any, carrier string) int {
	uses := 0
	rewrite := func(element any, set func(any)) {
		if s, ok := element.(string); ok {
			out, n := rewriteTemplate(s, carrier)
			if n > 0 {
				set(out)
				uses += n
			}
		} else {
			uses += rewriteJSONTemplates(element, carrier)
		}
	}
	switch v := value.(type) {
	case map[string]any:
		for key, element := range v {
			rewrite(element, func(out any) { v[key] = out })
		}
	case []any:
		for i, element := range v {
			rewrite(element, func(out any) { v[i] = out })
		}
	}
	return uses
}

func rewriteTemplate(s, carrier string) (string, int) {
	if !strings.Contains(s, projectIDAttribute) {
		return s, 0
	}
	expr, diags := hclsyntax.ParseTemplate([]byte(s), "", hcl.InitialPos)
	if diags.HasErrors() {
		return s, 0
	}
	replacements := projectIDReferences(expr, carrier)
	return string(replace([]byte(s), replacements)), len(replacements)
}
