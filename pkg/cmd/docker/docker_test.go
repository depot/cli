package docker

import (
	"reflect"
	"testing"
)

func TestDriverImageCandidates(t *testing.T) {
	repo := "public.ecr.aws/depot/cli:"

	tests := []struct {
		name    string
		version string
		want    []string
	}{
		{
			name:    "patch release falls back to major.minor and major",
			version: "2.101.69",
			want:    []string{repo + "2.101.69", repo + "2.101", repo + "2"},
		},
		{
			name:    "zero patch still yields distinct floating tags",
			version: "2.101.0",
			want:    []string{repo + "2.101.0", repo + "2.101", repo + "2"},
		},
		{
			name:    "major.minor.0 with zero minor",
			version: "3.0.0",
			want:    []string{repo + "3.0.0", repo + "3.0", repo + "3"},
		},
		{
			name:    "prerelease build tries exact tag only",
			version: "0.0.0-dev",
			want:    []string{repo + "0.0.0-dev"},
		},
		{
			name:    "non-semver tries exact tag only",
			version: "latest",
			want:    []string{repo + "latest"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := driverImageCandidates(tt.version)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("driverImageCandidates(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}
