package api

import (
	"net/http"
	"os"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/depot/cli/pkg/proto/depot/cli/v1beta1/cliv1beta1connect"
)

func NewBuildClient() cliv1connect.BuildServiceClient {
	baseURL := os.Getenv("DEPOT_API_URL")
	if baseURL == "" {
		baseURL = "https://api.depot.dev"
	}
	return cliv1connect.NewBuildServiceClient(http.DefaultClient, baseURL, WithUserAgent())
}

func NewLoginClient() cliv1beta1connect.LoginServiceClient {
	baseURL := os.Getenv("DEPOT_API_URL")
	if baseURL == "" {
		baseURL = "https://api.depot.dev"
	}
	return cliv1beta1connect.NewLoginServiceClient(http.DefaultClient, baseURL, WithUserAgent())
}

func NewProjectsClient() cliv1beta1connect.ProjectsServiceClient {
	baseURL := os.Getenv("DEPOT_API_URL")
	if baseURL == "" {
		baseURL = "https://api.depot.dev"
	}
	return cliv1beta1connect.NewProjectsServiceClient(http.DefaultClient, baseURL, WithUserAgent())
}

func NewPushClient() cliv1connect.PushServiceClient {
	baseURL := os.Getenv("DEPOT_API_URL")
	if baseURL == "" {
		baseURL = "https://api.depot.dev"
	}
	return cliv1connect.NewPushServiceClient(http.DefaultClient, baseURL, WithUserAgent())
}

func WithAuthentication[T any](req *connect.Request[T], token string) *connect.Request[T] {
	req.Header().Add("Authorization", "Bearer "+token)
	return req
}
