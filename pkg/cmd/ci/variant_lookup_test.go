package ci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	civ3beta2 "github.com/depot/cli/pkg/proto/depot/ci/v3beta2"
	"github.com/depot/cli/pkg/proto/depot/ci/v3beta2/civ3beta2connect"
	"github.com/spf13/cobra"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// The server shows a renamed repository under its current name, acme/new, and resolves --repo acme/old to it.
var renamedRepoVariant = []*civ3beta2.Attribute{{Key: "repository", Value: "acme/new"}}

type lookupSecretService struct {
	civ3beta2connect.UnimplementedSecretServiceHandler
	lists   []*civ3beta2.ListSecretsRequest
	deleted []string
}

func (s *lookupSecretService) ListSecrets(_ context.Context, req *connect.Request[civ3beta2.ListSecretsRequest]) (*connect.Response[civ3beta2.ListSecretsResponse], error) {
	s.lists = append(s.lists, req.Msg)
	return connect.NewResponse(&civ3beta2.ListSecretsResponse{Secrets: []*civ3beta2.Secret{{
		Id: "s1", Name: "TOKEN", VariantCount: 1,
		Variants: []*civ3beta2.SecretVariant{{Id: "v-renamed", SecretId: "s1", Name: "acme-old", Attributes: renamedRepoVariant}},
	}}}), nil
}

func (s *lookupSecretService) GetSecret(context.Context, *connect.Request[civ3beta2.GetSecretRequest]) (*connect.Response[civ3beta2.GetSecretResponse], error) {
	return connect.NewResponse(&civ3beta2.GetSecretResponse{Secret: &civ3beta2.Secret{
		Id: "s1", Name: "TOKEN", VariantCount: 2,
		Variants: []*civ3beta2.SecretVariant{
			{Id: "v-default", SecretId: "s1", Name: "default"},
			{Id: "v-renamed", SecretId: "s1", Name: "acme-old", Attributes: renamedRepoVariant},
		},
	}}), nil
}

func (s *lookupSecretService) DeleteSecretVariant(_ context.Context, req *connect.Request[civ3beta2.DeleteSecretVariantRequest]) (*connect.Response[civ3beta2.DeleteSecretVariantResponse], error) {
	s.deleted = append(s.deleted, req.Msg.GetVariantId())
	return connect.NewResponse(&civ3beta2.DeleteSecretVariantResponse{}), nil
}

type lookupVariableService struct {
	civ3beta2connect.UnimplementedVariableServiceHandler
	lists   []*civ3beta2.ListVariablesRequest
	deleted []string
}

func (s *lookupVariableService) ListVariables(_ context.Context, req *connect.Request[civ3beta2.ListVariablesRequest]) (*connect.Response[civ3beta2.ListVariablesResponse], error) {
	s.lists = append(s.lists, req.Msg)
	return connect.NewResponse(&civ3beta2.ListVariablesResponse{Variables: []*civ3beta2.Variable{{
		Id: "x1", Name: "TOKEN", VariantCount: 1,
		Variants: []*civ3beta2.VariableVariant{{Id: "v-renamed", VariableId: "x1", Name: "acme-old", Attributes: renamedRepoVariant}},
	}}}), nil
}

func (s *lookupVariableService) GetVariable(context.Context, *connect.Request[civ3beta2.GetVariableRequest]) (*connect.Response[civ3beta2.GetVariableResponse], error) {
	return connect.NewResponse(&civ3beta2.GetVariableResponse{Variable: &civ3beta2.Variable{
		Id: "x1", Name: "TOKEN", VariantCount: 2,
		Variants: []*civ3beta2.VariableVariant{
			{Id: "v-default", VariableId: "x1", Name: "default"},
			{Id: "v-renamed", VariableId: "x1", Name: "acme-old", Attributes: renamedRepoVariant},
		},
	}}), nil
}

func (s *lookupVariableService) DeleteVariableVariant(_ context.Context, req *connect.Request[civ3beta2.DeleteVariableVariantRequest]) (*connect.Response[civ3beta2.DeleteVariableVariantResponse], error) {
	s.deleted = append(s.deleted, req.Msg.GetVariantId())
	return connect.NewResponse(&civ3beta2.DeleteVariableVariantResponse{}), nil
}

func serveLookup(t *testing.T) (*lookupSecretService, *lookupVariableService) {
	t.Helper()
	secrets, variables := &lookupSecretService{}, &lookupVariableService{}
	mux := http.NewServeMux()
	mux.Handle(civ3beta2connect.NewSecretServiceHandler(secrets))
	mux.Handle(civ3beta2connect.NewVariableServiceHandler(variables))
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	return secrets, variables
}

func runLookupCmd(t *testing.T, newCmd func() *cobra.Command, args ...string) {
	t.Helper()
	cmd := newCmd()
	cmd.SetArgs(append(args, "--token", "depot_api_token", "--org", "org-id"))
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func assertOldRepoFilter(t *testing.T, query string, attributes, jobContext []*civ3beta2.Attribute, wantContext bool) {
	t.Helper()
	if query != "" && query != "TOKEN" {
		t.Fatalf("query = %q, want TOKEN", query)
	}
	want, other := attributes, jobContext
	if wantContext {
		want, other = jobContext, attributes
	}
	if len(want) != 1 || want[0].GetKey() != "repository" || want[0].GetValue() != "acme/old" || len(other) != 0 {
		t.Fatalf("attributes = %v, context = %v; want repository=acme/old as context=%v", attributes, jobContext, wantContext)
	}
}

func TestRemoveByOldRepoNameTrustsTheServerMatch(t *testing.T) {
	secrets, variables := serveLookup(t)
	runLookupCmd(t, NewCmdSecretsRemove, "TOKEN", "--repo", "acme/old", "--force")
	runLookupCmd(t, NewCmdVarsRemove, "TOKEN", "--repo", "acme/old", "--force")

	if len(secrets.lists) != 1 || len(variables.lists) != 1 {
		t.Fatalf("list calls = %d secrets, %d variables; want 1 each", len(secrets.lists), len(variables.lists))
	}
	assertOldRepoFilter(t, secrets.lists[0].GetQuery(), secrets.lists[0].GetAttributes(), secrets.lists[0].GetContext(), false)
	assertOldRepoFilter(t, variables.lists[0].GetQuery(), variables.lists[0].GetAttributes(), variables.lists[0].GetContext(), false)
	if len(secrets.deleted) != 1 || secrets.deleted[0] != "v-renamed" || len(variables.deleted) != 1 || variables.deleted[0] != "v-renamed" {
		t.Fatalf("deleted = %v secrets, %v variables; want the renamed repo's variant", secrets.deleted, variables.deleted)
	}
}

func TestGetByOldRepoNameTrustsTheServerMatch(t *testing.T) {
	secrets, _ := serveLookup(t)
	runLookupCmd(t, NewCmdSecretsGet, "TOKEN", "--repo", "acme/old", "--output", "json")

	if len(secrets.lists) != 1 || secrets.lists[0].GetQuery() != "TOKEN" {
		t.Fatalf("lists = %v, want one query for TOKEN", secrets.lists)
	}
	assertOldRepoFilter(t, "TOKEN", secrets.lists[0].GetAttributes(), secrets.lists[0].GetContext(), false)
}

func TestListSendsRepoAsJobContext(t *testing.T) {
	secrets, variables := serveLookup(t)
	runLookupCmd(t, NewCmdSecretsList, "--repo", "acme/old")
	runLookupCmd(t, NewCmdSecretsList, "TOKEN", "--repo", "acme/old")
	runLookupCmd(t, NewCmdVarsList, "--repo", "acme/old")
	runLookupCmd(t, NewCmdVarsList, "TOKEN", "--repo", "acme/old")

	if len(secrets.lists) != 2 || len(variables.lists) != 2 {
		t.Fatalf("list calls = %d secrets, %d variables; want 2 each", len(secrets.lists), len(variables.lists))
	}
	for _, req := range secrets.lists {
		assertOldRepoFilter(t, req.GetQuery(), req.GetAttributes(), req.GetContext(), true)
	}
	for _, req := range variables.lists {
		assertOldRepoFilter(t, req.GetQuery(), req.GetAttributes(), req.GetContext(), true)
	}
	if secrets.lists[1].GetQuery() != "TOKEN" || variables.lists[1].GetQuery() != "TOKEN" {
		t.Fatalf("named list queries = %q, %q; want TOKEN", secrets.lists[1].GetQuery(), variables.lists[1].GetQuery())
	}
}
