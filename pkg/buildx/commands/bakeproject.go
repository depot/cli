package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"regexp"
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
const projectIDAttribute = "project_id"

const composeProjectFileName = "depot-compose-projects.hcl"

type projectRewrite int

const (
	removeProjectID projectRewrite = iota
	projectIDToDescription
)

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

// targetDescriptions reads the description of each target, for bake --print.
func targetDescriptions(ctx context.Context, files []bake.File, targets, overrides []string, defaults map[string]string) (map[string]string, error) {
	descriptions := map[string]string{}
	if !slices.ContainsFunc(files, func(f bake.File) bool { return bytes.Contains(f.Data, []byte("description")) }) {
		return descriptions, nil
	}
	resolved, _, err := bake.ReadTargets(ctx, descriptionFiles(files), targets, overrides, defaults, nil, &bake.EntitlementConf{})
	if err != nil {
		return nil, err
	}
	for name, t := range resolved {
		descriptions[name] = t.Description
	}
	return descriptions, nil
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

	_ = hclsyntax.VisitAll(body, func(node hclsyntax.Node) hcl.Diagnostics {
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
			renames = append(renames, replacement{start: expr.SrcRange.Start.Byte, end: expr.SrcRange.End.Byte, text: []byte(`""`)})
		case projectIDToDescription:
			end := attr.SrcRange.End.Byte
			renames = append(renames, replacement{start: end - len(projectIDAttribute), end: end, text: []byte("description")})
		}
		return nil
	})

	replacements := blanks
	for _, r := range renames {
		inBlank := slices.ContainsFunc(blanks, func(b replacement) bool { return r.start >= b.start && r.end <= b.end })
		if !inBlank {
			replacements = append(replacements, r)
		}
	}
	slices.SortFunc(replacements, func(a, b replacement) int { return b.start - a.start })
	out := slices.Clone(data)
	for _, r := range replacements {
		out = slices.Concat(out[:r.start], r.text, out[r.end:])
	}
	return out, true
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

// projectIDReference matches a reference to the project_id of a target in
// an interpolation of a JSON definition.
var projectIDReference = regexp.MustCompile(`(target\.[A-Za-z0-9_-]+)\.project_id\b`)

func rewriteJSON(data []byte, mode projectRewrite) ([]byte, bool) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false
	}
	rawTargets, ok := root["target"]
	if !ok {
		return data, true
	}
	var targets map[string]map[string]json.RawMessage
	if err := json.Unmarshal(rawTargets, &targets); err != nil {
		return nil, false
	}

	changed := false
	for _, target := range targets {
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

	out := data
	if changed {
		encodedTargets, err := json.Marshal(targets)
		if err != nil {
			return nil, false
		}
		root["target"] = encodedTargets
		if out, err = json.Marshal(root); err != nil {
			return nil, false
		}
	}
	switch mode {
	case removeProjectID:
		out = projectIDReference.ReplaceAll(out, []byte(`\"\"`))
	case projectIDToDescription:
		out = projectIDReference.ReplaceAll(out, []byte("$1.description"))
	}
	return out, true
}
