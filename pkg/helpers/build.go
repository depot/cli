package helpers

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/bufbuild/connect-go"
	depotapi "github.com/depot/cli/pkg/api"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/moby/buildkit/util/grpcerrors"
	"google.golang.org/grpc/codes"
)

func BeginBuild(ctx context.Context, project string, token string) (buildID string, buildToken string, finishBuild func(buildErr error), err error) {
	client := depotapi.NewBuildClient()

	buildID = os.Getenv("DEPOT_BUILD_ID")
	buildToken = token

	if buildID == "" {
		req := cliv1.CreateBuildRequest{ProjectId: project}
		var b *connect.Response[cliv1.CreateBuildResponse]
		b, err = client.CreateBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), token))
		if err != nil {
			return "", "", nil, err
		}
		buildID = b.Msg.BuildId
		buildToken = b.Msg.BuildToken
	}

	finishBuild = func(buildErr error) {
		req := cliv1.FinishBuildRequest{BuildId: buildID}
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
		_, err := client.FinishBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), buildToken))
		if err != nil {
			log.Printf("error releasing builder: %v", err)
		}
	}

	return buildID, buildToken, finishBuild, err
}
