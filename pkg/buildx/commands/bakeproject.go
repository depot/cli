package commands

import (
	"bytes"
	"context"
	"encoding/json"
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
// Depot project of the target. Upstream bake rejects unknown attributes, so
// the files are rewritten before upstream bake parses them:
//
//   - withoutProjectIDs removes the attribute, for the build itself.
//   - projectIDsAsDescriptions moves the attribute into the description
//     attribute, so that upstream bake evaluates it with the same variables,
//     functions, inheritance, and matrix expansion as every other attribute.
//
// Compose services select a project with the x-depot.project-id build
// extension. Compose has no field that bake reads as a description, so
// composeProjectFile writes the project of each service as an HCL target
// description. Bake merges compose files before HCL files, so that HCL file
// comes first among the HCL files.
const projectIDAttribute = "project_id"

const composeProjectFileName = "depot-compose-projects.hcl"

type projectRewrite int

const (
	removeProjectID projectRewrite = iota
	projectIDToDescription
)

func withoutProjectIDs(files []bake.File) []bake.File {
	return rewriteFiles(files, removeProjectID)
}

func projectIDsAsDescriptions(files []bake.File) []bake.File {
	return rewriteFiles(files, projectIDToDescription)
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

	var replacements []replacement
	for _, block := range body.Blocks {
		if block.Type != "target" {
			continue
		}
		projectID, hasProjectID := block.Body.Attributes[projectIDAttribute]
		description, hasDescription := block.Body.Attributes["description"]
		switch mode {
		case removeProjectID:
			if hasProjectID {
				replacements = append(replacements, blank(data, projectID.SrcRange))
			}
		case projectIDToDescription:
			if hasDescription {
				replacements = append(replacements, blank(data, description.SrcRange))
			}
			if hasProjectID {
				replacements = append(replacements, replacement{
					start: projectID.NameRange.Start.Byte,
					end:   projectID.NameRange.End.Byte,
					text:  []byte("description"),
				})
			}
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
		switch mode {
		case removeProjectID:
			if hasProjectID {
				delete(target, projectIDAttribute)
				changed = true
			}
		case projectIDToDescription:
			if _, ok := target["description"]; ok {
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
	if !changed {
		return data, true
	}

	encodedTargets, err := json.Marshal(targets)
	if err != nil {
		return nil, false
	}
	root["target"] = encodedTargets
	out, err := json.Marshal(root)
	if err != nil {
		return nil, false
	}
	return out, true
}

// composeProjectFile returns an HCL file that sets the description of each
// compose service with an x-depot.project-id to that project. It returns
// false when no compose service selects a project.
func composeProjectFile(files []bake.File) (bake.File, bool, error) {
	composeTargets, err := compose.Targets(files)
	if err != nil {
		return bake.File{}, false, err
	}
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
	return bake.File{Name: composeProjectFileName, Data: f.Bytes()}, found, nil
}

// readTargetProjects returns the project_id of each target. Targets
// without a project_id are not in the map.
func readTargetProjects(ctx context.Context, files []bake.File, targets []string, defaults map[string]string) (map[string]string, error) {
	projects := map[string]string{}

	projectFile, hasComposeProjects, err := composeProjectFile(files)
	if err != nil {
		return nil, err
	}
	hasProjectID := slices.ContainsFunc(files, func(f bake.File) bool {
		return bytes.Contains(f.Data, []byte(projectIDAttribute))
	})
	if !hasProjectID && !hasComposeProjects {
		return projects, nil
	}

	projectFiles := projectIDsAsDescriptions(files)
	if hasComposeProjects {
		projectFiles = append([]bake.File{projectFile}, projectFiles...)
	}
	resolved, _, err := bake.ReadTargets(ctx, projectFiles, targets, nil, defaults, nil, &bake.EntitlementConf{})
	if err != nil {
		return nil, err
	}
	for name, t := range resolved {
		if t.Description != "" {
			projects[name] = t.Description
		}
	}
	return projects, nil
}
