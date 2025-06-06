package commands

import (
	"reflect"
	"testing"
)

func TestOverrides(t *testing.T) {
	tests := []struct {
		name string
		in   BakeOptions
		want []string
	}{
		{
			name: "no overrides",
			in:   BakeOptions{},
			want: nil,
		},
		{
			name: "export push",
			in: BakeOptions{
				commonOptions: commonOptions{exportPush: true},
			},
			want: []string{"*.push=true"},
		},
		{
			name: "no cache true",
			in: BakeOptions{
				commonOptions: commonOptions{noCache: boolPtr(true)},
			},
			want: []string{"*.no-cache=true"},
		},
		{
			name: "no cache false",
			in: BakeOptions{
				commonOptions: commonOptions{noCache: boolPtr(false)},
			},
			want: []string{"*.no-cache=false"},
		},
		{
			name: "pull true",
			in: BakeOptions{
				commonOptions: commonOptions{pull: boolPtr(true)},
			},
			want: []string{"*.pull=true"},
		},
		{
			name: "sbom override",
			in: BakeOptions{
				commonOptions: commonOptions{sbom: "true"},
			},
			want: []string{"*.attest=type=sbom,enabled=true"},
		},
		{
			name: "provenance override",
			in: BakeOptions{
				commonOptions: commonOptions{provenance: "true"},
			},
			want: []string{"*.attest=type=provenance,enabled=true"},
		},
		{
			name: "existing overrides",
			in: BakeOptions{
				overrides: []string{"target.dockerfile=Dockerfile.prod"},
			},
			want: []string{"target.dockerfile=Dockerfile.prod"},
		},
		{
			name: "combined overrides",
			in: BakeOptions{
				overrides:     []string{"target.dockerfile=Dockerfile.prod"},
				commonOptions: commonOptions{exportPush: true, pull: boolPtr(true)},
			},
			want: []string{"target.dockerfile=Dockerfile.prod", "*.push=true", "*.pull=true"},
		},
		{
			name: "all options",
			in: BakeOptions{
				overrides: []string{"custom.key=value"},
				commonOptions: commonOptions{
					exportPush: true,
					noCache:    boolPtr(true),
					pull:       boolPtr(false),
					sbom:       "scanner=syft",
					provenance: "mode=max",
				},
			},
			want: []string{
				"custom.key=value",
				"*.push=true",
				"*.no-cache=true",
				"*.pull=false",
				"*.attest=type=sbom,scanner=syft",
				"*.attest=type=provenance,mode=max",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := overrides(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("overrides() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsRemoteTarget(t *testing.T) {
	tests := []struct {
		name    string
		targets []string
		want    bool
	}{
		{
			name:    "empty targets",
			targets: []string{},
			want:    false,
		},
		{
			name:    "local target",
			targets: []string{"web"},
			want:    false,
		},
		{
			name:    "multiple local targets",
			targets: []string{"web", "api"},
			want:    false,
		},
		{
			name:    "http remote target",
			targets: []string{"https://github.com/user/repo.git"},
			want:    true,
		},
		{
			name:    "git remote target",
			targets: []string{"git://github.com/user/repo.git"},
			want:    true,
		},
		{
			name:    "github remote target",
			targets: []string{"github.com/user/repo"},
			want:    true,
		},
		{
			name:    "remote with local targets",
			targets: []string{"https://github.com/user/repo.git", "web"},
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRemoteTarget(tt.targets)
			if got != tt.want {
				t.Errorf("isRemoteTarget() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBakeOptionsStructure(t *testing.T) {
	// Test that BakeOptions has the expected structure
	opts := BakeOptions{
		files:     []string{"docker-bake.hcl"},
		overrides: []string{"*.platform=linux/amd64"},
		printOnly: true,
	}

	if len(opts.files) != 1 || opts.files[0] != "docker-bake.hcl" {
		t.Error("files field not working correctly")
	}

	if len(opts.overrides) != 1 || opts.overrides[0] != "*.platform=linux/amd64" {
		t.Error("overrides field not working correctly")
	}

	if !opts.printOnly {
		t.Error("printOnly field not working correctly")
	}
}

func TestNewLocalBakeValidator(t *testing.T) {
	options := BakeOptions{
		files: []string{"docker-bake.hcl"},
	}
	args := []string{"web", "api"}

	validator := NewLocalBakeValidator(options, args)

	if validator == nil {
		t.Fatal("NewLocalBakeValidator returned nil")
	}

	if !reflect.DeepEqual(validator.options, options) {
		t.Error("options not set correctly")
	}

	// Test that validator is properly initialized
	if validator.options.files == nil {
		t.Error("validator options not properly initialized")
	}
}

// Helper function for pointer to bool
func boolPtr(b bool) *bool {
	return &b
}
