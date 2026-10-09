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
// upstream bake knows. So the files are rewritten before upstream bake reads
// them:
//
//   - projectFiles moves project_id into the description attribute of each
//     target, and changes references to project_id into references to
//     description. Upstream bake then evaluates the project with the same
//     variables, functions, inheritance, and matrix expansion as every other
//     attribute. The description has no effect on a build, so these files
//     are used for the build too.
//   - descriptionFiles removes project_id and keeps the descriptions of the
//     user, for bake --print.
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

const composeProjectFileName = "depot-compose-projects.hcl"

type projectRewrite int

const (
	removeProjectID projectRewrite = iota
	projectIDToDescription
)

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
	return slices.ContainsFunc(files, func(f bake.File) bool { return !bytes.Equal(rewriteFile(f, removeProjectID), f.Data) }) ||
		slices.ContainsFunc(slices.Collect(maps.Values(composeTargets)), func(t compose.Target) bool { return t.ProjectID != "" })
}

// projectFiles returns the files with the project of each target in its
// description.
func projectFiles(files []bake.File, composeTargets map[string]compose.Target) []bake.File {
	out := rewriteFiles(files, projectIDToDescription)
	if f, ok := composeProjectFile(composeTargets); ok {
		out = append([]bake.File{f}, out...)
	}
	return out
}

// descriptionFiles returns the files without project_id.
func descriptionFiles(files []bake.File) []bake.File {
	return rewriteFiles(files, removeProjectID)
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
		f.Body().AppendNewBlock("target", []string{name}).Body().SetAttributeValue("description", cty.StringVal(projectID))
		found = true
	}
	return bake.File{Name: composeProjectFileName, Data: f.Bytes()}, found
}

func rewriteFiles(files []bake.File, mode projectRewrite) []bake.File {
	out := make([]bake.File, len(files))
	for i, f := range files {
		out[i] = bake.File{Name: f.Name, Data: rewriteFile(f, mode)}
	}
	return out
}

func rewriteFile(f bake.File, mode projectRewrite) []byte {
	if !bytes.Contains(f.Data, []byte(projectIDAttribute)) && mode == removeProjectID {
		return f.Data
	}
	if strings.HasSuffix(f.Name, ".json") {
		if data, ok := rewriteJSON(f.Data, mode); ok {
			return data
		}
		return f.Data
	}
	if data, ok := rewriteHCL(f.Data, f.Name, mode); ok {
		return data
	}
	if data, ok := rewriteJSON(f.Data, mode); ok {
		return data
	}
	return f.Data
}

type replacement struct {
	start, end int
	text       []byte
}

func rewriteHCL(data []byte, name string, mode projectRewrite) ([]byte, bool) {
	file, diags := hclsyntax.ParseConfig(data, name, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, false
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return nil, false
	}

	var blanks, renames []replacement
	for _, block := range body.Blocks {
		if block.Type != "target" {
			continue
		}
		projectID, hasProjectID := block.Body.Attributes[projectIDAttribute]
		description, hasDescription := block.Body.Attributes["description"]
		switch mode {
		case removeProjectID:
			if hasProjectID {
				blanks = append(blanks, blank(data, projectID.SrcRange))
			}
		case projectIDToDescription:
			if hasDescription {
				blanks = append(blanks, blank(data, description.SrcRange))
			}
			if hasProjectID {
				renames = append(renames, replacement{
					start: projectID.NameRange.Start.Byte,
					end:   projectID.NameRange.End.Byte,
					text:  []byte("description"),
				})
			}
		}
	}
	renames = append(renames, projectIDReferences(body, mode)...)

	replacements := blanks
	for _, r := range renames {
		inBlank := slices.ContainsFunc(blanks, func(b replacement) bool { return r.start >= b.start && r.end <= b.end })
		if !inBlank {
			replacements = append(replacements, r)
		}
	}
	return replace(data, replacements), true
}

// projectIDReferences returns the replacements for the expressions in node
// that read the project_id of a target.
func projectIDReferences(node hclsyntax.Node, mode projectRewrite) []replacement {
	var out []replacement
	_ = hclsyntax.VisitAll(node, func(node hclsyntax.Node) hcl.Diagnostics {
		expr, ok := node.(*hclsyntax.ScopeTraversalExpr)
		if !ok || expr.Traversal.RootName() != "target" || len(expr.Traversal) < 3 {
			return nil
		}
		attr, ok := expr.Traversal[2].(hcl.TraverseAttr)
		if !ok || attr.Name != projectIDAttribute {
			return nil
		}
		switch mode {
		case removeProjectID:
			out = append(out, replacement{start: expr.SrcRange.Start.Byte, end: expr.SrcRange.End.Byte, text: []byte(`""`)})
		case projectIDToDescription:
			end := attr.SrcRange.End.Byte
			out = append(out, replacement{start: end - len(projectIDAttribute), end: end, text: []byte("description")})
		}
		return nil
	})
	return out
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
func rewriteJSON(data []byte, mode projectRewrite) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}

	changed := false
	targets, _ := root["target"].(map[string]any)
	for _, value := range targets {
		target, ok := value.(map[string]any)
		if !ok {
			continue
		}
		projectID, hasProjectID := target[projectIDAttribute]
		_, hasDescription := target["description"]
		switch mode {
		case removeProjectID:
			if hasProjectID {
				delete(target, projectIDAttribute)
				changed = true
			}
		case projectIDToDescription:
			if hasDescription {
				delete(target, "description")
				changed = true
			}
			if hasProjectID {
				delete(target, projectIDAttribute)
				target["description"] = projectID
				changed = true
			}
		}
	}
	if rewriteJSONTemplates(root, mode) {
		changed = true
	}
	if !changed {
		return data, true
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, false
	}
	return out, true
}

// rewriteJSONTemplates rewrites the references to project_id in every
// string of a decoded JSON value. Bake reads each string as a template.
func rewriteJSONTemplates(value any, mode projectRewrite) bool {
	changed := false
	rewrite := func(element any, set func(any)) {
		if s, ok := element.(string); ok {
			if out, ok := rewriteTemplate(s, mode); ok {
				set(out)
				changed = true
			}
		} else if rewriteJSONTemplates(element, mode) {
			changed = true
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
	return changed
}

func rewriteTemplate(s string, mode projectRewrite) (string, bool) {
	if !strings.Contains(s, projectIDAttribute) {
		return s, false
	}
	expr, diags := hclsyntax.ParseTemplate([]byte(s), "", hcl.InitialPos)
	if diags.HasErrors() {
		return s, false
	}
	replacements := projectIDReferences(expr, mode)
	if len(replacements) == 0 {
		return s, false
	}
	return string(replace([]byte(s), replacements)), true
}
