package pull

import (
	"reflect"
	"testing"

	"github.com/depot/cli/pkg/load"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
)

// Define a simplified type for testing
type pullOptions = load.PullOptions

func TestIsSavedBuild(t *testing.T) {
	tests := []struct {
		name         string
		options      []*cliv1.BuildOptions
		savedForLoad bool
		want         bool
	}{
		{
			name:         "no options, savedForLoad false",
			options:      []*cliv1.BuildOptions{},
			savedForLoad: false,
			want:         false,
		},
		{
			name:         "no options, savedForLoad true",
			options:      []*cliv1.BuildOptions{},
			savedForLoad: true,
			want:         true,
		},
		{
			name: "option with save true",
			options: []*cliv1.BuildOptions{
				{Save: true},
			},
			savedForLoad: false,
			want:         true,
		},
		{
			name: "option with save false",
			options: []*cliv1.BuildOptions{
				{Save: false},
			},
			savedForLoad: false,
			want:         false,
		},
		{
			name: "multiple options, one with save true",
			options: []*cliv1.BuildOptions{
				{Save: false},
				{Save: true},
				{Save: false},
			},
			savedForLoad: false,
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSavedBuild(tt.options, tt.savedForLoad)
			if got != tt.want {
				t.Errorf("isSavedBuild() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsBake(t *testing.T) {
	tests := []struct {
		name    string
		options []*cliv1.BuildOptions
		want    bool
	}{
		{
			name:    "no options",
			options: []*cliv1.BuildOptions{},
			want:    false,
		},
		{
			name: "build command",
			options: []*cliv1.BuildOptions{
				{Command: cliv1.Command_COMMAND_BUILD},
			},
			want: false,
		},
		{
			name: "bake command",
			options: []*cliv1.BuildOptions{
				{Command: cliv1.Command_COMMAND_BAKE},
			},
			want: true,
		},
		{
			name: "multiple options with bake",
			options: []*cliv1.BuildOptions{
				{Command: cliv1.Command_COMMAND_BUILD},
				{Command: cliv1.Command_COMMAND_BAKE},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBake(tt.options)
			if got != tt.want {
				t.Errorf("isBake() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildPullOpt(t *testing.T) {
	msg := &cliv1.GetPullInfoResponse{
		Reference: "registry.depot.dev/project123/build456",
		Username:  "depot",
		Password:  "token123",
		Options: []*cliv1.BuildOptions{
			{Tags: []string{"myapp:latest", "myapp:v1.0"}},
		},
	}

	tests := []struct {
		name     string
		userTags []string
		platform string
		progress string
		want     *pull
	}{
		{
			name:     "no user tags, use build tags",
			userTags: []string{},
			platform: "",
			progress: "auto",
			want: &pull{
				imageName: "registry.depot.dev/project123/build456",
				pullOptions: pullOptionsWithDefaults([]string{"myapp:latest", "myapp:v1.0"}, "", false),
			},
		},
		{
			name:     "user tags override build tags",
			userTags: []string{"custom:tag"},
			platform: "",
			progress: "auto",
			want: &pull{
				imageName: "registry.depot.dev/project123/build456",
				pullOptions: pullOptionsWithDefaults([]string{"custom:tag"}, "", false),
			},
		},
		{
			name:     "with platform",
			userTags: []string{"custom:tag"},
			platform: "linux/amd64",
			progress: "auto",
			want: &pull{
				imageName: "registry.depot.dev/project123/build456",
				pullOptions: pullOptionsWithDefaults([]string{"custom:tag"}, "linux/amd64", false),
			},
		},
		{
			name:     "quiet progress",
			userTags: []string{"custom:tag"},
			platform: "",
			progress: "quiet",
			want: &pull{
				imageName: "registry.depot.dev/project123/build456",
				pullOptions: pullOptionsWithDefaults([]string{"custom:tag"}, "", true),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildPullOpt(msg, tt.userTags, tt.platform, tt.progress)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildPullOpt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateTargets(t *testing.T) {
	msg := &cliv1.GetPullInfoResponse{
		Options: []*cliv1.BuildOptions{
			{TargetName: stringPtr("web")},
			{TargetName: stringPtr("api")},
			{TargetName: stringPtr("worker")},
		},
	}

	tests := []struct {
		name    string
		targets []string
		wantErr bool
	}{
		{
			name:    "no targets specified",
			targets: []string{},
			wantErr: false,
		},
		{
			name:    "valid single target",
			targets: []string{"web"},
			wantErr: false,
		},
		{
			name:    "valid multiple targets",
			targets: []string{"web", "api"},
			wantErr: false,
		},
		{
			name:    "invalid target",
			targets: []string{"invalid"},
			wantErr: true,
		},
		{
			name:    "mix of valid and invalid targets",
			targets: []string{"web", "invalid"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTargets(tt.targets, msg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTargets() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBakePullOpts(t *testing.T) {
	msg := &cliv1.GetPullInfoResponse{
		Reference: "registry.depot.dev/project123/build456",
		Username:  "depot",
		Password:  "token123",
		Options: []*cliv1.BuildOptions{
			{
				TargetName: stringPtr("web"),
				Tags:       []string{"myapp-web:latest"},
			},
			{
				TargetName: stringPtr("api"),
				Tags:       []string{"myapp-api:latest"},
			},
		},
	}

	tests := []struct {
		name     string
		targets  []string
		userTags []string
		platform string
		progress string
		want     []*pull
	}{
		{
			name:     "all targets, no user tags",
			targets:  []string{},
			userTags: []string{},
			platform: "",
			progress: "auto",
			want: []*pull{
				{
					imageName: "registry.depot.dev/project123/build456-web",
					pullOptions: pullOptionsWithDefaults([]string{"myapp-web:latest"}, "", false),
				},
				{
					imageName: "registry.depot.dev/project123/build456-api",
					pullOptions: pullOptionsWithDefaults([]string{"myapp-api:latest"}, "", false),
				},
			},
		},
		{
			name:     "specific target",
			targets:  []string{"web"},
			userTags: []string{},
			platform: "",
			progress: "auto",
			want: []*pull{
				{
					imageName: "registry.depot.dev/project123/build456-web",
					pullOptions: pullOptionsWithDefaults([]string{"myapp-web:latest"}, "", false),
				},
			},
		},
		{
			name:     "multiple targets with user tags",
			targets:  []string{},
			userTags: []string{"custom:v1"},
			platform: "",
			progress: "auto",
			want: []*pull{
				{
					imageName: "registry.depot.dev/project123/build456-web",
					pullOptions: pullOptionsWithDefaults([]string{"custom:v1-web"}, "", false),
				},
				{
					imageName: "registry.depot.dev/project123/build456-api",
					pullOptions: pullOptionsWithDefaults([]string{"custom:v1-api"}, "", false),
				},
			},
		},
		{
			name:     "single target with user tags",
			targets:  []string{"web"},
			userTags: []string{"custom:v1"},
			platform: "",
			progress: "auto",
			want: []*pull{
				{
					imageName: "registry.depot.dev/project123/build456-web",
					pullOptions: pullOptionsWithDefaults([]string{"custom:v1"}, "", false),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bakePullOpts(msg, tt.targets, tt.userTags, tt.platform, tt.progress)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("bakePullOpts() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Helper functions for tests
func stringPtr(s string) *string {
	return &s
}

func pullOptionsWithDefaults(tags []string, platform string, quiet bool) pullOptions {
	username := "depot"
	password := "token123"
	serverAddress := "registry.depot.dev"
	
	opts := pullOptions{
		UserTags:      tags,
		Quiet:         quiet,
		KeepImage:     true,
		Username:      &username,
		Password:      &password,
		ServerAddress: &serverAddress,
	}
	
	if platform != "" {
		opts.Platform = &platform
	}
	
	return opts
}