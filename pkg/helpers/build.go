package helpers

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/bufbuild/connect-go"
	depotapi "github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/profiler"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/moby/buildkit/util/grpcerrors"
	"google.golang.org/grpc/codes"
)

type Build struct {
	ID               string
	Token            string
	UseLocalRegistry bool
	Finish           func(error)
}

func BeginBuild(ctx context.Context, project string, token string) (build Build, err error) {
	client := depotapi.NewBuildClient()

	build.Token = token
	build.ID = os.Getenv("DEPOT_BUILD_ID")
	if build.ID == "" {
		req := cliv1.CreateBuildRequest{ProjectId: project}
		var b *connect.Response[cliv1.CreateBuildResponse]
		b, err = client.CreateBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), token))
		if err != nil {
			return build, err
		}
		build.ID = b.Msg.BuildId
		build.Token = b.Msg.BuildToken

		build.UseLocalRegistry = b.Msg.GetRegistry() != nil && b.Msg.GetRegistry().CanUseLocalRegistry
		if os.Getenv("DEPOT_USE_LOCAL_REGISTRY") != "" {
			build.UseLocalRegistry = true
		}
	}

	profiler.StartProfiler(build.ID)
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
