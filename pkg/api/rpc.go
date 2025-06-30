package api

import (
	"net/http"
	"os"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/depot/cli/pkg/proto/depot/cli/v1beta1/cliv1beta1connect"
)

func NewBuildClient() cliv1connect.BuildServiceClient {
	return cliv1connect.NewBuildServiceClient(http.DefaultClient, getBaseURL(), WithUserAgent())
}

func NewLoginClient() cliv1beta1connect.LoginServiceClient {
	return cliv1beta1connect.NewLoginServiceClient(http.DefaultClient, getBaseURL(), WithUserAgent())
}

func NewProjectsClient() cliv1beta1connect.ProjectsServiceClient {
	return cliv1beta1connect.NewProjectsServiceClient(http.DefaultClient, getBaseURL(), WithUserAgent())
}

func NewSDKProjectsClient() corev1connect.ProjectServiceClient {
	return corev1connect.NewProjectServiceClient(http.DefaultClient, getBaseURL(), WithUserAgent())
}

func NewPushClient() cliv1connect.PushServiceClient {
	return cliv1connect.NewPushServiceClient(http.DefaultClient, getBaseURL(), WithUserAgent())
}

func NewClaudeClient() agentv1connect.ClaudeServiceClient {
	return agentv1connect.NewClaudeServiceClient(http.DefaultClient, getBaseURL(), WithUserAgent())
}

func WithAuthentication[T any](req *connect.Request[T], token string) *connect.Request[T] {
	req.Header().Add("Authorization", "Bearer "+token)
	return req
}

func getBaseURL() string {
	baseURL := os.Getenv("DEPOT_API_URL")
	if baseURL == "" {
		baseURL = "https://api.depot.dev"
	}
	return baseURL
}
