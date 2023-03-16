package commands

import (
	"testing"

	"github.com/docker/buildx/build"
	"github.com/moby/buildkit/client"
	"github.com/stretchr/testify/assert"
)

func TestWithDepotImagePull(t *testing.T) {
	type args struct {
		buildOpts    map[string]build.Options
		depotOpts    DepotOptions
		progressMode string
	}
	tests := []struct {
		name          string
		args          args
		wantBuildOpts map[string]build.Options
		wantPullOpts  []PullOptions
	}{
		{
			name: "No exports pushes to depot registry but has no tags",
			args: args{
				buildOpts: map[string]build.Options{defaultTargetName: {}},
				depotOpts: DepotOptions{
					buildID:       "bid1",
					registryURL:   "https://depot.dev",
					registryToken: "hunter2",
				},
				progressMode: "auto",
			},
			wantBuildOpts: map[string]build.Options{
				defaultTargetName: {
					Exports: []client.ExportEntry{
						{
							Type:  "image",
							Attrs: map[string]string{"name": "https://depot.dev/your-image:bid1", "push": "true"},
						},
					},
				},
			},
			wantPullOpts: []PullOptions{
				{
					DepotTag:           "https://depot.dev/your-image:bid1",
					DepotRegistryURL:   "https://depot.dev",
					DepotRegistryToken: "hunter2",
					Quiet:              false,
				},
			},
		},
		{
			name: "Multiple tags are allowed.",
			args: args{
				buildOpts: map[string]build.Options{
					defaultTargetName: {
						Tags: []string{
							"my-registry.com/your-image:latest",
							"my-registry.com/your-image:v2",
						},
					},
				},
				depotOpts: DepotOptions{
					buildID:       "bid1",
					registryURL:   "https://depot.dev",
					registryToken: "hunter2",
				},
				progressMode: "auto",
			},
			wantBuildOpts: map[string]build.Options{
				defaultTargetName: {
					Exports: []client.ExportEntry{
						{
							Type:  "image",
							Attrs: map[string]string{"name": "https://depot.dev/your-image:bid1", "push": "true"},
						},
					},
					Tags: []string{
						"my-registry.com/your-image:latest",
						"my-registry.com/your-image:v2",
					},
				},
			},
			wantPullOpts: []PullOptions{
				{
					UserTags: []string{
						"my-registry.com/your-image:latest",
						"my-registry.com/your-image:v2",
					},
					DepotTag:           "https://depot.dev/your-image:bid1",
					DepotRegistryURL:   "https://depot.dev",
					DepotRegistryToken: "hunter2",
					Quiet:              false,
				},
			},
		},
		{
			name: "Buildkit cannot support more than one type of exporter right now, so, we should not send to depot registry",
			args: args{
				buildOpts: map[string]build.Options{
					defaultTargetName: {
						Exports: []client.ExportEntry{
							{
								Type: "local",
							},
						},
					},
				},
				depotOpts: DepotOptions{
					buildID:       "bid1",
					registryURL:   "https://depot.dev",
					registryToken: "hunter2",
				},
				progressMode: "auto",
			},
			wantBuildOpts: map[string]build.Options{
				defaultTargetName: {
					Exports: []client.ExportEntry{
						{
							Type: "local",
						},
					},
				},
			},
			wantPullOpts: []PullOptions{},
		},
		{
			name: "If there is already an image exporter we are able to send to depot registry still because it is the same type.",
			args: args{
				buildOpts: map[string]build.Options{
					defaultTargetName: {
						Exports: []client.ExportEntry{
							{
								Type: "image",
							},
						},
					},
				},
				depotOpts: DepotOptions{
					buildID:       "bid1",
					registryURL:   "https://depot.dev",
					registryToken: "hunter2",
				},
				progressMode: "auto",
			},
			wantBuildOpts: map[string]build.Options{
				defaultTargetName: {
					Exports: []client.ExportEntry{
						{
							Type:  "image",
							Attrs: map[string]string{"name": "https://depot.dev/your-image:bid1", "push": "true"},
						},
					},
				},
			},
			wantPullOpts: []PullOptions{
				{
					DepotTag:           "https://depot.dev/your-image:bid1",
					DepotRegistryURL:   "https://depot.dev",
					DepotRegistryToken: "hunter2",
					Quiet:              false,
				},
			},
		},
		{
			name: "If we are already pushing to a different registry we need to add depot registry as well",
			args: args{
				buildOpts: map[string]build.Options{
					defaultTargetName: {
						Exports: []client.ExportEntry{
							{
								Type: "image",
								Attrs: map[string]string{
									"name": "my-registry.com/your-image:latest",
									"push": "true",
								},
							},
						},
					},
				},
				depotOpts: DepotOptions{
					buildID:       "bid1",
					registryURL:   "https://depot.dev",
					registryToken: "hunter2",
				},
				progressMode: "auto",
			},
			wantBuildOpts: map[string]build.Options{
				defaultTargetName: {
					Exports: []client.ExportEntry{
						{
							Type: "image",
							Attrs: map[string]string{
								"name": "my-registry.com/your-image:latest,https://depot.dev/your-image:bid1",
								"push": "true",
							},
						},
					},
				},
			},
			wantPullOpts: []PullOptions{
				{
					UserTags:           []string{"my-registry.com/your-image:latest"},
					DepotTag:           "https://depot.dev/your-image:bid1",
					DepotRegistryURL:   "https://depot.dev",
					DepotRegistryToken: "hunter2",
					Quiet:              false,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBuildOpts, gotPullOpts := WithDepotImagePull(tt.args.buildOpts, tt.args.depotOpts, tt.args.progressMode)
			assert.Equal(t, tt.wantBuildOpts, gotBuildOpts)
			assert.Equal(t, tt.wantPullOpts, gotPullOpts)
		})
	}
}
