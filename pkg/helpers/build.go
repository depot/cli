package helpers

import (
	"context"
	"log"
	"os"

	"github.com/bufbuild/connect-go"
	depotapi "github.com/depot/cli/pkg/api"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
)

func BeginBuild(ctx context.Context, project string, token string) (buildID string, finishBuild func(buildErr error), err error) {
	client := depotapi.NewBuildClient()

	buildID = os.Getenv("DEPOT_BUILD_ID")
	if buildID == "" {
		req := cliv1beta1.CreateBuildRequest{ProjectId: project}
		var b *connect.Response[cliv1beta1.CreateBuildResponse]
		b, err = client.CreateBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), token))
		if err != nil {
			return "", nil, err
		}
		buildID = b.Msg.BuildId
	}

	finishBuild = func(buildErr error) {
		req := cliv1beta1.FinishBuildRequest{BuildId: buildID}
		req.Result = &cliv1beta1.FinishBuildRequest_Success{Success: &cliv1beta1.FinishBuildRequest_BuildSuccess{}}
		if buildErr != nil {
			errorMessage := buildErr.Error()
			req.Result = &cliv1beta1.FinishBuildRequest_Error{Error: &cliv1beta1.FinishBuildRequest_BuildError{Error: errorMessage}}
		}
		_, err := client.FinishBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), token))
		if err != nil {
			log.Printf("error releasing builder: %v", err)
		}
	}

	return buildID, finishBuild, err
}
