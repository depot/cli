package api

import (
	"fmt"
	"net/http"
	"os"
	"runtime"

	"github.com/bufbuild/connect-go"
	"github.com/depot/cli/internal/build"
	"github.com/depot/cli/pkg/proto/depot/cli/v1beta1/cliv1beta1connect"
)

func NewBuildClient() cliv1beta1connect.BuildServiceClient {
	baseURL := os.Getenv("DEPOT_API_URL")
	if baseURL == "" {
		baseURL = "https://api.depot.dev"
	}
	return cliv1beta1connect.NewBuildServiceClient(http.DefaultClient, baseURL, connect.WithGRPC())
}

func NewLoginClient() cliv1beta1connect.LoginServiceClient {
	baseURL := os.Getenv("DEPOT_API_URL")
	if baseURL == "" {
		baseURL = "https://api.depot.dev"
	}
	return cliv1beta1connect.NewLoginServiceClient(http.DefaultClient, baseURL, connect.WithGRPC())
}

func NewProjectsClient() cliv1beta1connect.ProjectsServiceClient {
	baseURL := os.Getenv("DEPOT_API_URL")
	if baseURL == "" {
		baseURL = "https://api.depot.dev"
	}
	return cliv1beta1connect.NewProjectsServiceClient(http.DefaultClient, baseURL, connect.WithGRPC())
}

func WithHeaders[T any](req *connect.Request[T], token string) *connect.Request[T] {
	req.Header().Add("User-Agent", fmt.Sprintf("depot-cli/%s/%s/%s", build.Version, runtime.GOOS, runtime.GOARCH))
	req.Header().Add("Depot-User-Agent", fmt.Sprintf("depot-cli/%s/%s/%s", build.Version, runtime.GOOS, runtime.GOARCH))
	if token != "" {
		req.Header().Add("Authorization", "Bearer "+token)
	}
	return req
}
