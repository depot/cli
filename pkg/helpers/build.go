package helpers

import (
	"context"
	"log"
	"os"

	"github.com/bufbuild/connect-go"
	depotapi "github.com/depot/cli/pkg/api"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
)

type Build struct {
	ID            string
	RegistryURL   string
	RegistryToken string
	Finish        func(error)
}

func BeginBuild(ctx context.Context, project string, token string) (build Build, err error) {
	client := depotapi.NewBuildClient()

	build.ID = os.Getenv("DEPOT_BUILD_ID")
	if build.ID == "" {
		req := cliv1.CreateBuildRequest{ProjectId: project}
		var b *connect.Response[cliv1.CreateBuildResponse]
		b, err = client.CreateBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), token))
		if err != nil {
			return build, err
		}
		build.ID = b.Msg.BuildId

		registry := b.Msg.GetRegistry()
		if registry != nil {
			build.RegistryURL = registry.Url
			build.RegistryToken = registry.Token
		}
	}

	build.Finish = func(buildErr error) {
		req := cliv1.FinishBuildRequest{BuildId: build.ID}
		req.Result = &cliv1.FinishBuildRequest_Success{Success: &cliv1.FinishBuildRequest_BuildSuccess{}}
		if buildErr != nil {
			errorMessage := buildErr.Error()
			req.Result = &cliv1.FinishBuildRequest_Error{Error: &cliv1.FinishBuildRequest_BuildError{Error: errorMessage}}
		}
		_, err := client.FinishBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), token))
		if err != nil {
			log.Printf("error releasing builder: %v", err)
		}
	}

	return build, err
}
