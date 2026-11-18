package code

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	gitv1 "github.com/depot/cli/pkg/proto/depot/git/v1"
	"github.com/depot/cli/pkg/proto/depot/git/v1/gitv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func readRPC[Req, Resp any](t *testing.T, mux *http.ServeMux, path string, fn func(*Req) *Resp) {
	t.Helper()
	mux.Handle(path, connect.NewUnaryHandler(path, func(_ context.Context, req *connect.Request[Req]) (*connect.Response[Resp], error) {
		if req.Header().Get("Authorization") != "Bearer test-token" || req.Header().Get("X-Depot-Org") != "test-org" {
			t.Error("incorrect auth scope")
		}
		return connect.NewResponse(fn(req.Msg)), nil
	}))
}
func readServer(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	return mux
}
func readCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return execute(t, append(args, "--org", "test-org", "--token", "test-token", "--output", "json")...)
}

func TestViewEmptyRepositoryByIDAliasAndQualifiedRemote(t *testing.T) {
	mux := readServer(t)
	repo := &code.Repository{RepositoryId: "repo1", Name: "team/café", DefaultBranch: "develop"}
	readRPC(t, mux, codev1beta2connect.CodeServiceGetRepositoryProcedure, func(req *code.GetRepositoryRequest) *code.GetRepositoryResponse {
		if req.GetName() != "old-name" && req.GetRepositoryId() != "repo1" {
			t.Errorf("unexpected lookup %v", req)
		}
		return &code.GetRepositoryResponse{Repository: repo}
	})
	readRPC(t, mux, gitv1connect.RepositoryServiceResolveProcedure, func(req *gitv1.ResolveRequest) *gitv1.ResolveResponse {
		if req.Repository.GetName() != "old-name" {
			t.Errorf("unexpected remote %v", req)
		}
		return &gitv1.ResolveResponse{Repository: &gitv1.Repository{Id: "repo1", OrgId: "test-org", CanonicalName: repo.Name}}
	})
	for _, selector := range [][]string{{"old-name"}, {"--id", "repo1"}, {"--remote", "https://test-org.code.depot.dev/old-name"}} {
		out, err := readCommand(t, append([]string{"repo", "view"}, selector...)...)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]string
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if got["repositoryId"] != "repo1" || got["name"] != repo.Name || got["defaultBranch"] != "develop" || got["cloneUrl"] != "https://test-org.code.depot.dev/team/caf%C3%A9" || got["webUrl"] != "https://depot.dev/orgs/test-org/code/repositories/repo1" {
			t.Fatalf("bad repository %s", out)
		}
	}
}

func TestRepositoryListPreservesPagination(t *testing.T) {
	mux := readServer(t)
	readRPC(t, mux, codev1beta2connect.CodeServiceListRepositoriesProcedure, func(req *code.ListRepositoriesRequest) *code.ListRepositoriesResponse {
		if req.PageSize != 2 {
			t.Errorf("unexpected page size %d", req.PageSize)
		}
		if req.PageToken == "second" {
			return &code.ListRepositoriesResponse{}
		}
		if req.PageToken != "" {
			t.Errorf("unexpected token %q", req.PageToken)
		}
		return &code.ListRepositoriesResponse{Repositories: []*code.Repository{{RepositoryId: "repo1", Name: "widgets", DefaultBranch: "trunk"}}, NextPageToken: "second"}
	})
	for _, page := range []string{"", "second"} {
		out, err := readCommand(t, "repo", "list", "--limit", "2", "--page-token", page)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Repositories  []json.RawMessage
			NextPageToken string
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if page == "" && (len(got.Repositories) != 1 || got.NextPageToken != "second") {
			t.Fatalf("lost pagination: %s", out)
		}
		if page == "second" && (len(got.Repositories) != 0 || got.NextPageToken != "" || !strings.Contains(out, `"repositories":[]`)) {
			t.Fatalf("bad empty page: %s", out)
		}
	}
}

func TestRepositoryViewRejectsConflictingScopeAndSelectors(t *testing.T) {
	mux := readServer(t)
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("unexpected API call") })
	for _, args := range [][]string{
		{"repo", "view", "name", "--id", "repo1"}, {"repo", "view", "--id", ""}, {"repo", "view", "--remote", "https://other-org.code.depot.dev/widgets"},
		{"repo", "list", "--limit", "0"}, {"repo", "list", "--limit", "501"},
	} {
		if out, err := readCommand(t, args...); err == nil || out != "" {
			t.Fatalf("expected refusal: %s %v", out, err)
		}
	}
}

func TestRepositoryLookupRejectsInvalidResponsesWithoutPrintingThem(t *testing.T) {
	for _, repo := range []*code.Repository{nil, {RepositoryId: "other", Name: "widgets"}, {RepositoryId: "repo1", Name: "../secret"}, {RepositoryId: "repo1", Name: "widgets\x1b[2J"}, {RepositoryId: "repo1", Name: "widgets", DefaultBranch: "main\x1b[2J"}} {
		t.Run(repo.GetName()+repo.GetDefaultBranch(), func(t *testing.T) {
			mux := readServer(t)
			readRPC(t, mux, codev1beta2connect.CodeServiceGetRepositoryProcedure, func(*code.GetRepositoryRequest) *code.GetRepositoryResponse {
				return &code.GetRepositoryResponse{Repository: repo}
			})
			out, err := readCommand(t, "repo", "view", "--id", "repo1")
			if err == nil || out != "" || strings.Contains(err.Error(), "\x1b") {
				t.Fatalf("unsafe response: %q %v", out, err)
			}
		})
	}
}
