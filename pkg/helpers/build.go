package helpers

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/bufbuild/connect-go"
	depotapi "github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/load"
	"github.com/depot/cli/pkg/profiler"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/docker/buildx/build"
	"github.com/moby/buildkit/util/grpcerrors"
	"google.golang.org/grpc/codes"
)

type Build struct {
	ID               string
	Token            string
	UseLocalRegistry bool
	ProxyImage       string
	// BuildURL is the URL to the build on the depot web UI.
	BuildURL string
	Finish   func(error)
}

func BeginBuild(ctx context.Context, req *cliv1.CreateBuildRequest, token string) (build Build, err error) {
	client := depotapi.NewBuildClient()

	build.Token = token
	build.ID = os.Getenv("DEPOT_BUILD_ID")
	profilerToken := ""
	if build.ID == "" {
		var b *connect.Response[cliv1.CreateBuildResponse]
		b, err = client.CreateBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(req), token))
		if err != nil {
			return build, err
		}
		build.ID = b.Msg.BuildId
		build.Token = b.Msg.BuildToken

		if b.Msg.Profiler != nil {
			profilerToken = b.Msg.Profiler.Token
		}

		build.UseLocalRegistry = b.Msg.GetRegistry() != nil && b.Msg.GetRegistry().CanUseLocalRegistry
		if os.Getenv("DEPOT_USE_LOCAL_REGISTRY") != "" {
			build.UseLocalRegistry = true
		}

		if b.Msg.GetRegistry() != nil {
			build.ProxyImage = b.Msg.GetRegistry().ProxyImage
		}
		if proxyImage := os.Getenv("DEPOT_PROXY_IMAGE"); proxyImage != "" {
			build.ProxyImage = proxyImage
		}
		if build.ProxyImage == "" {
			build.ProxyImage = load.DefaultProxyImageName
		}

		build.BuildURL = b.Msg.BuildUrl
	}

	profiler.StartProfiler(build.ID, profilerToken)

	build.Finish = func(buildErr error) {
		req := cliv1.FinishBuildRequest{BuildId: build.ID}
		req.Result = &cliv1.FinishBuildRequest_Success{Success: &cliv1.FinishBuildRequest_BuildSuccess{}}
		if buildErr != nil {
			// Classify errors as canceled by user/ci or build error.
			if errors.Is(buildErr, context.Canceled) {
				// Context canceled would happen for steps that are not buildkitd.
				req.Result = &cliv1.FinishBuildRequest_Canceled{Canceled: &cliv1.FinishBuildRequest_BuildCanceled{}}
			} else if status, ok := grpcerrors.AsGRPCStatus(buildErr); ok && status.Code() == codes.Canceled {
				// Cancelled by buildkitd happens during a remote buildkitd step.
				req.Result = &cliv1.FinishBuildRequest_Canceled{Canceled: &cliv1.FinishBuildRequest_BuildCanceled{}}
			} else {
				errorMessage := buildErr.Error()
				req.Result = &cliv1.FinishBuildRequest_Error{Error: &cliv1.FinishBuildRequest_BuildError{Error: errorMessage}}
			}
		}
		_, err := client.FinishBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), build.Token))
		if err != nil {
			log.Printf("error releasing builder: %v", err)
		}
	}

	return build, err
}

type UsingDepotFeatures struct {
	Push bool
	Load bool
	Lint bool
}

func NewBuildRequest(project string, opts map[string]build.Options, features UsingDepotFeatures) *cliv1.CreateBuildRequest {
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

func NewBakeRequest(project string, opts map[string]build.Options, features UsingDepotFeatures) *cliv1.CreateBuildRequest {
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
