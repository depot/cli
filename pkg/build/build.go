package build

import (
	"context"
	"errors"
	"log"

	"github.com/bufbuild/connect-go"
	depotapi "github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/progress"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
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
	Reporter *progress.Progress

	Response *connect.Response[cliv1.CreateBuildResponse]
}

type Credential struct {
	Host  string
	Token string
}

func (b *Build) AdditionalTags() []string {
	if b.Response == nil || b.Response.Msg == nil {
		return nil
	}

	tags := make([]string, 0, len(b.Response.Msg.AdditionalTags))
	for _, tag := range b.Response.Msg.AdditionalTags {
		if tag == nil {
			continue
		}

		tags = append(tags, tag.Tag)
	}

	return tags
}

func (b *Build) AdditionalCredentials() []Credential {
	if b.Response == nil || b.Response.Msg == nil {
		return nil
	}
	if len(b.Response.Msg.AdditionalCredentials) == 0 {
		return nil
	}

	creds := make([]Credential, 0, len(b.Response.Msg.AdditionalCredentials))

	for _, cred := range b.Response.Msg.AdditionalCredentials {
		if cred == nil {
			continue
		}

		creds = append(creds, Credential{
			Host:  cred.Host,
			Token: cred.Token,
		})
	}

	return creds
}

// BuildProject returns the project ID to be used for the build.
// This is important as the API may use a different project ID than the one
// initially requested (e.g. onboarding)
func (b *Build) BuildProject() string {
	if b.Response == nil || b.Response.Msg == nil {
		return ""
	}
	return b.Response.Msg.ProjectId
}

func NewBuild(ctx context.Context, req *cliv1.CreateBuildRequest, token string) (Build, error) {
	client := depotapi.NewBuildClient()
	res, err := client.CreateBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(req), token))
	if err != nil {
		return Build{}, err
	}

	build, err := FromExistingBuild(ctx, res.Msg.BuildId, res.Msg.BuildToken)
	if err != nil {
		return Build{}, err
	}

	build.Response = res
	build.BuildURL = res.Msg.BuildUrl
	build.UseLocalRegistry = res.Msg.GetRegistry() != nil && res.Msg.GetRegistry().CanUseLocalRegistry
	if res.Msg.GetRegistry() != nil {
		build.ProxyImage = res.Msg.GetRegistry().ProxyImage
	}

	return build, nil
}

func FromExistingBuild(ctx context.Context, buildID, token string) (Build, error) {
	finish := func(buildErr error) {
		client := depotapi.NewBuildClient()
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
		_, err := client.FinishBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), token))
		if err != nil {
			log.Printf("error releasing builder: %v", err)
		}
	}

	return Build{
		ID:     buildID,
		Token:  token,
		Finish: finish,
	}, nil
}
