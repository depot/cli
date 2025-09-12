package api

import (
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"strings"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"github.com/depot/cli/pkg/proto/depot/build/v1/buildv1connect"
	"github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/depot/cli/pkg/proto/depot/cli/v1beta1/cliv1beta1connect"
	cliCorev1connect "github.com/depot/cli/pkg/proto/depot/core/v1/corev1connect"
	"golang.org/x/net/http2"
)

func NewBuildClient() cliv1connect.BuildServiceClient {
	return cliv1connect.NewBuildServiceClient(getHTTPClient(getBaseURL()), getBaseURL(), WithUserAgent())
}

func NewLoginClient() cliv1beta1connect.LoginServiceClient {
	return cliv1beta1connect.NewLoginServiceClient(getHTTPClient(getBaseURL()), getBaseURL(), WithUserAgent())
}

func NewProjectsClient() cliv1beta1connect.ProjectsServiceClient {
	return cliv1beta1connect.NewProjectsServiceClient(getHTTPClient(getBaseURL()), getBaseURL(), WithUserAgent())
}

func NewOrganizationsClient() cliCorev1connect.OrganizationServiceClient {
	return cliCorev1connect.NewOrganizationServiceClient(getHTTPClient(getBaseURL()), getBaseURL(), WithUserAgent())
}

func NewSDKProjectsClient() corev1connect.ProjectServiceClient {
	return corev1connect.NewProjectServiceClient(getHTTPClient(getBaseURL()), getBaseURL(), WithUserAgent())
}

func NewPushClient() cliv1connect.PushServiceClient {
	return cliv1connect.NewPushServiceClient(getHTTPClient(getBaseURL()), getBaseURL(), WithUserAgent())
}

func NewClaudeClient() agentv1connect.ClaudeServiceClient {
	return agentv1connect.NewClaudeServiceClient(getHTTPClient(getBaseURL()), getBaseURL(), WithUserAgent())
}

func NewSessionClient() agentv1connect.SessionServiceClient {
	return agentv1connect.NewSessionServiceClient(getHTTPClient(getBaseURL()), getBaseURL(), WithUserAgent())
}

func NewSandboxClient() agentv1connect.SandboxServiceClient {
	return agentv1connect.NewSandboxServiceClient(getHTTPClient(getBaseURL()), getBaseURL(), WithUserAgent())
}

func NewRegistryClient() buildv1connect.RegistryServiceClient {
	return buildv1connect.NewRegistryServiceClient(getHTTPClient(getBaseURL()), getBaseURL(), WithUserAgent())
}

func WithAuthentication[T any](req *connect.Request[T], token string) *connect.Request[T] {
	req.Header().Add("Authorization", "Bearer "+token)
	return req
}

func WithAuthenticationAndOrg[T any](req *connect.Request[T], token, orgID string) *connect.Request[T] {
	req.Header().Add("Authorization", "Bearer "+token)
	if orgID != "" {
		req.Header().Add("x-depot-org", orgID)
	}
	return req
}

// getHTTPClient returns an HTTP client configured for the given base URL.
// If the URL uses HTTP (not HTTPS), it configures h2c support for HTTP/2 over cleartext.
func getHTTPClient(baseURL string) *http.Client {
	if strings.HasPrefix(baseURL, "http://") {
		// Configure h2c (HTTP/2 over cleartext) for non-TLS connections
		return &http.Client{
			Transport: &http2.Transport{
				AllowHTTP: true,
				DialTLS: func(network, addr string, _ *tls.Config) (net.Conn, error) {
					return net.Dial(network, addr)
				},
			},
		}
	}
	// Use default client for HTTPS connections
	return http.DefaultClient
}

func getBaseURL() string {
	baseURL := os.Getenv("DEPOT_API_URL")
	if baseURL == "" {
		baseURL = "https://api.depot.dev"
	}
	return baseURL
}
