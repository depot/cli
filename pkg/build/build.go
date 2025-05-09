package build

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"
	depotapi "github.com/depot/cli/pkg/api"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/util/progress"
	clitypes "github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/util/grpcerrors"
	"google.golang.org/grpc/codes"
)

type Build struct {
	ID    string
	Token string
	// BuildURL is the URL to the build on the depot web UI.
	BuildURL string
	Finish   func(error)
	Reporter progress.Writer

	Response  *connect.Response[cliv1.CreateBuildResponse]
	projectID string
}

type Credential struct {
	Host  string
	Token string
}

type PullInfo struct {
	Reference string
	Username  string
	Password  string
}

func (b *Build) AdditionalTags() []string {
	if b.Response == nil || b.Response.Msg == nil {
		return []string{fmt.Sprintf("registry.depot.dev/%s:%s", b.projectID, b.ID)}
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
		token := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("x-token:%s", b.Token)))
		return []Credential{{Host: "registry.depot.dev", Token: token}}
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

func (b *Build) AuthProvider(dockerAuth driver.Auth) driver.Auth {
	return NewAuthProvider(b.AdditionalCredentials(), dockerAuth)
}

// BuildProject returns the project ID to be used for the build.
// This is important as the API may use a different project ID than the one
// initially requested (e.g. onboarding)
func (b *Build) BuildProject() string {
	if b.projectID != "" {
		return b.projectID
	}

	if b.Response == nil || b.Response.Msg == nil {
		return ""
	}
	return b.Response.Msg.ProjectId
}

func (b *Build) LoadUsingRegistry() bool {
	if b.Response == nil || b.Response.Msg == nil {
		return false
	}

	registry := b.Response.Msg.GetRegistry()
	if registry == nil {
		return false
	}

	return registry.LoadUsingRegistry
}

func NewBuild(ctx context.Context, req *cliv1.CreateBuildRequest, token string) (Build, error) {
	client := depotapi.NewBuildClient()
	res, err := client.CreateBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(req), token))
	if err != nil {
		return Build{}, err
	}

	build, err := FromExistingBuild(ctx, res.Msg.BuildId, res.Msg.BuildToken, res)
	if err != nil {
		return Build{}, err
	}

	return build, nil
}

func FromExistingBuild(ctx context.Context, buildID, token string, buildRes *connect.Response[cliv1.CreateBuildResponse]) (Build, error) {
	client := depotapi.NewBuildClient()

	finish := func(buildErr error) {
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

	if buildRes == nil {
		req := cliv1.GetBuildRequest{BuildId: buildID}
		res, err := client.GetBuild(ctx, depotapi.WithAuthentication(connect.NewRequest(&req), token))
		if err != nil {
			return Build{}, err
		}
		return Build{
			ID:        buildID,
			Token:     token,
			Finish:    finish,
			BuildURL:  res.Msg.BuildUrl,
			projectID: res.Msg.ProjectId,
		}, nil
	} else {
		return Build{
			ID:       buildID,
			Token:    token,
			Finish:   finish,
			BuildURL: buildRes.Msg.BuildUrl,
			Response: buildRes,
		}, nil
	}

}

func PullBuildInfo(ctx context.Context, buildID, token string) (*PullInfo, error) {
	// Download location and credentials of image save.
	client := depotapi.NewBuildClient()
	req := &cliv1.GetPullInfoRequest{BuildId: buildID}
	res, err := client.GetPullInfo(ctx, depotapi.WithAuthentication(connect.NewRequest(req), token))
	if err != nil {
		return nil, err
	}

	return &PullInfo{
		Reference: res.Msg.Reference,
		Username:  res.Msg.Username,
		Password:  res.Msg.Password,
	}, nil
}

type authProvider struct {
	credentials map[string]clitypes.AuthConfig
	dockerAuth  driver.Auth
}

func NewAuthProvider(credentials []Credential, dockerAuth driver.Auth) driver.Auth {
	parsed := make(map[string]clitypes.AuthConfig, len(credentials))
	for _, cred := range credentials {
		decodedAuth, err := base64.StdEncoding.DecodeString(cred.Token)
		if err != nil {
			continue
		}

		usernamePassword := strings.SplitN(string(decodedAuth), ":", 2)
		if len(usernamePassword) != 2 {
			continue
		}

		parsed[cred.Host] = clitypes.AuthConfig{
			Username: usernamePassword[0],
			Password: usernamePassword[1],
		}
	}

	return authProvider{
		credentials: parsed,
		dockerAuth:  dockerAuth,
	}
}

func (a authProvider) GetAuthConfig(registryHostname string) (clitypes.AuthConfig, error) {
	for host, cred := range a.credentials {
		if host == registryHostname {
			return cred, nil
		}
	}

	return a.dockerAuth.GetAuthConfig(registryHostname)
}
