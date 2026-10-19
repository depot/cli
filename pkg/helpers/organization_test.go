package helpers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/spf13/viper"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type organizationTestHandler struct{}

func (organizationTestHandler) ListOrganizations(
	_ context.Context,
	req *connect.Request[corev1.ListOrganizationsRequest],
) (*connect.Response[corev1.ListOrganizationsResponse], error) {
	if req.Header().Get("Authorization") != "Bearer organization-test-token" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid token"))
	}
	return connect.NewResponse(&corev1.ListOrganizationsResponse{
		Organizations: []*corev1.ListOrganizationsResponse_Organization{
			{OrgId: "org-1", Name: "First organization"},
			{OrgId: "org-2", Name: "Second organization"},
		},
	}), nil
}

func TestRetrieveOrganizationsUsesPublishedService(t *testing.T) {
	path, handler := corev1connect.NewOrganizationServiceHandler(organizationTestHandler{})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)

	previousToken := viper.Get("api_token")
	viper.Set("api_token", "organization-test-token")
	t.Cleanup(func() { viper.Set("api_token", previousToken) })

	organizations, err := RetrieveOrganizations()
	if err != nil {
		t.Fatal(err)
	}
	want := []*Organization{
		{OrgId: "org-1", Name: "First organization"},
		{OrgId: "org-2", Name: "Second organization"},
	}
	if !reflect.DeepEqual(organizations, want) {
		t.Fatalf("organizations = %#v, want %#v", organizations, want)
	}
}
