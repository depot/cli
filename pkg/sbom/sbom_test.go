package sbom

import (
	"os"
	"reflect"
	"sort"
	"testing"
)

func Test_writeSBOMs(t *testing.T) {
	tests := []struct {
		name            string
		targetPlatforms map[string]map[string]SBOM
		wantFiles       []string
		wantErr         bool
	}{
		{
			name: "single target, single platform",
			targetPlatforms: map[string]map[string]SBOM{
				"target": {
					"platform": SBOM{Statement: []byte("sbom")},
				},
			},
			wantFiles: []string{"sbom.spdx.json"},
			wantErr:   false,
		},
		{
			name: "single target, multiple platforms",
			targetPlatforms: map[string]map[string]SBOM{
				"target": {
					"platform1": SBOM{Statement: []byte("sbom1")},
					"platform2": SBOM{Statement: []byte("sbom2")},
				},
			},
			wantFiles: []string{"platform1.spdx.json", "platform2.spdx.json"},
			wantErr:   false,
		},
		{
			name: "multiple targets, single platform",
			targetPlatforms: map[string]map[string]SBOM{
				"target1": {
					"platform": SBOM{Statement: []byte("sbom1")},
				},
				"target2": {
					"platform": SBOM{Statement: []byte("sbom2")},
				},
			},
			wantFiles: []string{"target1.spdx.json", "target2.spdx.json"},
			wantErr:   false,
		},
		{
			name: "multiple targets, multiple platforms",
			targetPlatforms: map[string]map[string]SBOM{
				"target1": {
					"platform1": SBOM{Statement: []byte("sbom1")},
					"platform2": SBOM{Statement: []byte("sbom2")},
				},
				"target2": {
					"platform1": SBOM{Statement: []byte("sbom3")},
					"platform2": SBOM{Statement: []byte("sbom4")},
				},
			},
			wantFiles: []string{
				"target1_platform1.spdx.json",
				"target1_platform2.spdx.json",
				"target2_platform1.spdx.json",
				"target2_platform2.spdx.json",
			},
			wantErr: false,
		},
		{
			name: "no platforms",
			targetPlatforms: map[string]map[string]SBOM{
				"target1": {},
				"target2": {},
			},
			wantFiles: []string{},
			wantErr:   false,
		},
		{
			name:            "no targets",
			targetPlatforms: map[string]map[string]SBOM{},
			wantFiles:       []string{},
			wantErr:         false,
		},
		{
			name:      "nil targetPlatforms",
			wantFiles: []string{},
			wantErr:   false,
		},
		{
			name: "nil platforms",
			targetPlatforms: map[string]map[string]SBOM{
				"target1": nil,
				"target2": nil,
			},
			wantFiles: []string{},
			wantErr:   false,
		},
		{
			name: "nil sbom",
			targetPlatforms: map[string]map[string]SBOM{
				"target1": {
					"platform1": SBOM{Statement: nil},
				},
			},
			wantFiles: []string{},
			wantErr:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputDir := t.TempDir()
			if err := writeSBOMs(tt.targetPlatforms, outputDir); (err != nil) != tt.wantErr {
				t.Errorf("writeSBOMs() error = %v, wantErr %v", err, tt.wantErr)
			}

			dir, err := os.Open(outputDir)
			if err != nil {
				t.Errorf("writeSBOMs() error = %v", err)
			}

			files, err := dir.Readdirnames(0)
			if err != nil {
				t.Errorf("writeSBOMs() error = %v", err)
			}
			sort.Strings(files)
			if len(files) != len(tt.wantFiles) {
				t.Errorf("writeSBOMs() files = %v, want %v", files, tt.wantFiles)
			}

			// Compare files
			if !reflect.DeepEqual(files, tt.wantFiles) {
				t.Errorf("writeSBOMs() files = %v, want %v", files, tt.wantFiles)
			}
		})
	}
}
