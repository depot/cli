package helpers

import (
	"context"
	"os"

	depotbuild "github.com/depot/cli/pkg/build"
	"github.com/depot/cli/pkg/profiler"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	buildx "github.com/docker/buildx/build"
)

func BeginBuild(ctx context.Context, req *cliv1.CreateBuildRequest, token string) (depotbuild.Build, error) {
	var build depotbuild.Build
	var err error
	if id := os.Getenv("DEPOT_BUILD_ID"); id != "" {
		build, err = depotbuild.FromExistingBuild(ctx, id, token)
	} else {
		build, err = depotbuild.NewBuild(ctx, req, token)
	}
	if err != nil {
		return depotbuild.Build{}, err
	}

	profilerToken := ""
	if build.Response != nil && build.Response.Msg != nil && build.Response.Msg.Profiler != nil {
		profilerToken = build.Response.Msg.Profiler.Token
	}

	if os.Getenv("DEPOT_USE_LOCAL_REGISTRY") != "" {
		build.UseLocalRegistry = true
	}

	if proxyImage := os.Getenv("DEPOT_PROXY_IMAGE"); proxyImage != "" {
		build.ProxyImage = proxyImage
	}

	profiler.StartProfiler(build.ID, profilerToken)

	return build, err
}

type UsingDepotFeatures struct {
	Push bool
	Load bool
	Lint bool
}

func NewBuildRequest(project string, opts map[string]buildx.Options, features UsingDepotFeatures) *cliv1.CreateBuildRequest {
	// There is only one target for a build request, "default".
	for _, opts := range opts {
		outputs := make([]*cliv1.BuildOutput, len(opts.Exports))
		for i := range opts.Exports {
			outputs[i] = &cliv1.BuildOutput{
				Kind:       opts.Exports[i].Type,
				Attributes: opts.Exports[i].Attrs,
			}
		}

		var target *string
		if opts.Target != "" {
			target = &opts.Target
		}

		return &cliv1.CreateBuildRequest{
			ProjectId: project,
			Options: []*cliv1.BuildOptions{
				{
					Command:    cliv1.Command_COMMAND_BUILD,
					Tags:       opts.Tags,
					Outputs:    outputs,
					Push:       features.Push,
					Load:       features.Load,
					Lint:       features.Lint,
					TargetName: target,
				},
			},
		}
	}

	// Should never be reached.
	return &cliv1.CreateBuildRequest{ProjectId: project}
}

func NewBakeRequest(project string, opts map[string]buildx.Options, features UsingDepotFeatures) *cliv1.CreateBuildRequest {
	targets := make([]*cliv1.BuildOptions, 0, len(opts))

	for name, opts := range opts {
		name := name
		outputs := make([]*cliv1.BuildOutput, len(opts.Exports))
		for i := range opts.Exports {
			outputs[i] = &cliv1.BuildOutput{
				Kind:       opts.Exports[i].Type,
				Attributes: opts.Exports[i].Attrs,
			}
		}

		targets = append(targets, &cliv1.BuildOptions{
			Command:    cliv1.Command_COMMAND_BAKE,
			Tags:       opts.Tags,
			Outputs:    outputs,
			Push:       features.Push,
			Load:       features.Load,
			Lint:       features.Lint,
			TargetName: &name,
		})
	}

	return &cliv1.CreateBuildRequest{
		ProjectId: project,
		Options:   targets,
	}
}
