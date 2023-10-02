package sbom

import (
	"path"
	"reflect"
	"sort"
	"testing"
)

func Test_withSBOMPaths(t *testing.T) {
	tests := []struct {
		name            string
		targetPlatforms map[string]map[string]sbomOutput
		wantFiles       []string
	}{
		{
			name: "single target, single platform",
			targetPlatforms: map[string]map[string]sbomOutput{
				"target": {
					"platform": {},
				},
			},
			wantFiles: []string{"sbom.spdx.json"},
		},
		{
			name: "single target, multiple platforms",
			targetPlatforms: map[string]map[string]sbomOutput{
				"target": {
					"platform1": {},
					"platform2": {},
				},
			},
			wantFiles: []string{"platform1.spdx.json", "platform2.spdx.json"},
		},
		{
			name: "multiple targets, single platform",
			targetPlatforms: map[string]map[string]sbomOutput{
				"target1": {
					"platform": {},
				},
				"target2": {
					"platform": {},
				},
			},
			wantFiles: []string{"target1.spdx.json", "target2.spdx.json"},
		},
		{
			name: "multiple targets, multiple platforms",
			targetPlatforms: map[string]map[string]sbomOutput{
				"target1": {
					"platform1": {},
					"platform2": {},
				},
				"target2": {
					"platform1": {},
					"platform2": {},
				},
			},
			wantFiles: []string{
				"target1_platform1.spdx.json",
				"target1_platform2.spdx.json",
				"target2_platform1.spdx.json",
				"target2_platform2.spdx.json",
			},
		},
		{
			name: "no platforms",
			targetPlatforms: map[string]map[string]sbomOutput{
				"target1": {},
				"target2": {},
			},
			wantFiles: []string{},
		},
		{
			name:            "no targets",
			targetPlatforms: map[string]map[string]sbomOutput{},
			wantFiles:       []string{},
		},
		{
			name:      "nil targetPlatforms",
			wantFiles: []string{},
		},
		{
			name: "nil platforms",
			targetPlatforms: map[string]map[string]sbomOutput{
				"target1": nil,
				"target2": nil,
			},
			wantFiles: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputDir := t.TempDir()
			output := withSBOMPaths(tt.targetPlatforms, outputDir)

			files := []string{}
			for _, sbom := range output {
				files = append(files, sbom.outputPath)
			}

			wantFiles := []string{}
			for _, file := range tt.wantFiles {
				wantFiles = append(wantFiles, path.Join(outputDir, file))
			}

			sort.Strings(wantFiles)
			sort.Strings(files)
			if !reflect.DeepEqual(files, wantFiles) {
				t.Errorf("withSBOMPaths() files = %v, want %v", files, wantFiles)
			}
		})
	}
}
