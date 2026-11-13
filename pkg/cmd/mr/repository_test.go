package mr

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	gitv1 "github.com/depot/cli/pkg/proto/depot/git/v1"
	"github.com/depot/cli/pkg/proto/depot/git/v1/gitv1connect"
	"github.com/spf13/viper"
	"google.golang.org/protobuf/proto"
)

func executeInferred(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewCmdMR()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"create", "--title", "Test change", "--org", "test-org", "--token", "local-test-token"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func mirror(id, upstream string) *gitv1.Repository {
	owner, name, _ := strings.Cut(upstream, "/")
	return &gitv1.Repository{Id: id, OrgId: "test-org", CanonicalName: "renamed/" + id,
		Upstream: &gitv1.RepositoryUpstream{Provider: gitv1.UpstreamProvider_UPSTREAM_PROVIDER_GITHUB, Owner: owner, Name: name}}
}

func expectCreate(t *testing.T, mux *http.ServeMux, repo string) *int {
	t.Helper()
	calls := new(int)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(req *code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		if req.RepositoryId != repo || req.HeadRef != "feature" || req.Title != "Test change" {
			t.Errorf("wrong inferred creation target: %v", req)
		}
		*calls++
		return &code.CreateMergeRequestResponse{MergeRequest: fixture("abc")}, nil
	})
	return calls
}

func TestCreateInfersDepotRemoteNames(t *testing.T) {
	for _, tc := range []struct{ url, name string }{
		{"https://test-org.code.depot.dev/widgets", "widgets"},
		{"https://user:secret@code.depot.dev/team/widgets.git", "team/widgets.git"},
		{"ssh://git@TEST-ORG.code.depot.dev:2222/team/widgets", "team/widgets"},
		{"git@test-org.code.depot.dev:widgets.git", "widgets.git"},
		{"https://TEST-ORG.CODE.DEPOT.DEV.:443/team/widgets/", "team/widgets"},
	} {
		t.Run(tc.url, func(t *testing.T) {
			checkout(t)
			runGit(t, "tag", "feature")
			runGit(t, "remote", "add", "origin", tc.url)
			mux := apiServer(t)
			rpc(t, mux, gitv1connect.RepositoryServiceResolveProcedure, func(req *gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
				if req.Repository.GetName() != tc.name || req.Repository.GetId() != "" {
					t.Errorf("wrong name selector: %v", req)
				}
				return &gitv1.ResolveResponse{Repository: &gitv1.Repository{Id: "opaque-id", OrgId: "test-org"}}, nil
			})
			calls := expectCreate(t, mux, "opaque-id")
			out, err := executeInferred(t)
			if err != nil || *calls != 1 || !strings.Contains(out, "mr_1") {
				t.Fatalf("create: %q %v, calls %d", out, err, *calls)
			}
		})
	}
}

func TestCreatePairsRemotesAndDeduplicatesDepotAliases(t *testing.T) {
	checkout(t)
	for name, url := range map[string]string{
		"origin": "https://github.com/ACME/Widgets.git",
		"depot":  "https://test-org.code.depot.dev/renamed",
		"alias":  "git@code.depot.dev:old-name",
	} {
		runGit(t, "remote", "add", name, url)
	}
	mux := apiServer(t)
	rpc(t, mux, gitv1connect.RepositoryServiceResolveProcedure, func(*gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
		return &gitv1.ResolveResponse{Repository: mirror("opaque-id", "acme/widgets")}, nil
	})
	mux.HandleFunc(gitv1connect.RepositoryServiceListProcedure, func(http.ResponseWriter, *http.Request) {
		t.Error("paired Depot remote should not require owner-only listing")
	})
	calls := expectCreate(t, mux, "opaque-id")
	if out, err := executeInferred(t); err != nil || *calls != 1 || !strings.Contains(out, "mr_1") {
		t.Fatalf("create: %q %v, calls %d", out, err, *calls)
	}
}

func TestCreateUsesDepotRemoteWithUnmirroredGitHubFork(t *testing.T) {
	for _, mirrored := range []bool{false, true} {
		for _, canList := range []bool{false, true} {
			t.Run(fmt.Sprintf("mirrored=%v/canList=%v", mirrored, canList), func(t *testing.T) {
				checkout(t)
				runGit(t, "remote", "add", "depot", "https://test-org.code.depot.dev/widgets")
				runGit(t, "remote", "add", "origin", "git@github.com:alice/widgets.git")
				mux := apiServer(t)
				rpc(t, mux, gitv1connect.RepositoryServiceResolveProcedure, func(*gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
					repo := &gitv1.Repository{Id: "opaque-id", OrgId: "test-org"}
					if mirrored {
						repo = mirror("opaque-id", "acme/widgets")
					}
					return &gitv1.ResolveResponse{Repository: repo}, nil
				})
				rpc(t, mux, gitv1connect.RepositoryServiceListProcedure, func(*gitv1.ListRequest) (*gitv1.ListResponse, error) {
					if !canList {
						return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner access required"))
					}
					return &gitv1.ListResponse{Repositories: []*gitv1.Repository{mirror("unrelated", "other/widgets")}}, nil
				})
				calls := expectCreate(t, mux, "opaque-id")
				if out, err := executeInferred(t); err != nil || *calls != 1 || !strings.Contains(out, "mr_1") {
					t.Fatalf("create with fork: %q %v, calls %d", out, err, *calls)
				}
			})
		}
	}
}

func TestCreateReadsOptionLikeRemotesAndSkipsUnreadableNames(t *testing.T) {
	for _, name := range []string{"-mirror", "origin"} {
		t.Run(name, func(t *testing.T) {
			checkout(t)
			runGit(t, "remote", "add", "--", name, "https://test-org.code.depot.dev/widgets")
			// Malformed config names are listed by Git but their individual words
			// cannot be looked up as remotes.
			runGit(t, "config", "remote.bad name.url", "https://example.test/widgets")
			mux := apiServer(t)
			rpc(t, mux, gitv1connect.RepositoryServiceResolveProcedure, func(*gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
				return &gitv1.ResolveResponse{Repository: &gitv1.Repository{Id: "opaque-id", OrgId: "test-org"}}, nil
			})
			calls := expectCreate(t, mux, "opaque-id")
			if out, err := executeInferred(t); err != nil || *calls != 1 || !strings.Contains(out, "mr_1") {
				t.Fatalf("create with irregular remotes: %q %v, calls %d", out, err, *calls)
			}
		})
	}
}

func TestCreateRejectsBlankRepositoryBeforeAuthentication(t *testing.T) {
	checkout(t)
	for _, name := range []string{"DEPOT_TOKEN", "DEPOT_API_TOKEN", "DEPOT_JIT_TOKEN", "DEPOT_CACHE_TOKEN", "DEPOT_RUN_TOKEN_URL"} {
		t.Setenv(name, "")
	}
	viper.Set("api_token", "")
	t.Cleanup(viper.Reset)
	mux := apiServer(t)
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("unexpected authentication or repository request") })
	for _, repo := range []string{"", " "} {
		out, err := executeInferred(t, "--repo", repo, "--token", "")
		if err == nil || !strings.Contains(err.Error(), "--repo must not be blank") || out != "" {
			t.Fatalf("expected input validation before authentication: %q %v", out, err)
		}
	}
}

func TestCreateFindsExistingMirrorAcrossAllPages(t *testing.T) {
	checkout(t)
	runGit(t, "remote", "add", "origin", "ssh://git@github.com/Acme/Widgets.git")
	runGit(t, "remote", "add", "duplicate", "git@github.com:acme/widgets.git")
	mux := apiServer(t)
	pages := 0
	rpc(t, mux, gitv1connect.RepositoryServiceListProcedure, func(req *gitv1.ListRequest) (*gitv1.ListResponse, error) {
		pages++
		switch req.GetPageToken() {
		case "":
			// A matching canonical name alone does not make this a mirror.
			return &gitv1.ListResponse{Repositories: []*gitv1.Repository{{Id: "unrelated", OrgId: "test-org", CanonicalName: "acme/widgets"}}, NextPageToken: proto.String("next")}, nil
		case "next":
			return &gitv1.ListResponse{Repositories: []*gitv1.Repository{mirror("opaque-id", "ACME/Widgets")}, NextPageToken: proto.String("last")}, nil
		case "last":
			return &gitv1.ListResponse{}, nil
		default:
			t.Fatalf("unexpected page token %q", req.GetPageToken())
			return nil, nil
		}
	})
	calls := expectCreate(t, mux, "opaque-id")
	if out, err := executeInferred(t); err != nil || *calls != 1 || pages != 3 || !strings.Contains(out, "mr_1") {
		t.Fatalf("create: %q %v, calls %d, pages %d", out, err, *calls, pages)
	}
}

func TestCreateExplicitRepositorySkipsAllLookups(t *testing.T) {
	for _, withCheckout := range []bool{false, true} {
		t.Run(fmt.Sprint(withCheckout), func(t *testing.T) {
			if withCheckout {
				checkout(t)
				runGit(t, "remote", "add", "origin", "https://different-org.code.depot.dev/unrelated")
			} else {
				t.Chdir(t.TempDir())
			}
			mux := apiServer(t)
			mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("explicit --repo triggered an API lookup") })
			calls := expectCreate(t, mux, "explicit-id")
			if _, err := executeInferred(t, "--repo", "explicit-id", "--head", "feature"); err != nil || *calls != 1 {
				t.Fatalf("explicit repository: %v, calls %d", err, *calls)
			}
		})
	}
}

func TestCreateInferenceUsesCurrentOrganization(t *testing.T) {
	checkout(t)
	runGit(t, "remote", "add", "origin", "https://test-org.code.depot.dev/widgets")
	viper.Set("org_id", "test-org")
	t.Cleanup(viper.Reset)
	mux := apiServer(t)
	rpc(t, mux, gitv1connect.RepositoryServiceResolveProcedure, func(*gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
		return &gitv1.ResolveResponse{Repository: &gitv1.Repository{Id: "opaque-id", OrgId: "test-org"}}, nil
	})
	calls := expectCreate(t, mux, "opaque-id")
	if _, err := executeInferred(t, "--org", ""); err != nil || *calls != 1 {
		t.Fatalf("current organization: %v, calls %d", err, *calls)
	}
}

func TestCreateInferenceRequiresOrganizationAndNonblankOverride(t *testing.T) {
	checkout(t)
	runGit(t, "remote", "add", "origin", "https://code.depot.dev/widgets")
	viper.Set("org_id", "")
	t.Cleanup(viper.Reset)
	mux := apiServer(t)
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("unexpected API request") })
	for _, args := range [][]string{{"--org", ""}, {"--repo", ""}, {"--repo", " "}} {
		if out, err := executeInferred(t, args...); err == nil || !strings.Contains(err.Error(), "--repo") || out != "" {
			t.Fatalf("expected local refusal for %v: %q %v", args, out, err)
		}
	}
}

func TestCreateInferenceRefusesUnsafeTargets(t *testing.T) {
	for _, tc := range []struct {
		name    string
		remotes []string
		resolve func(*gitv1.ResolveRequest) (*gitv1.ResolveResponse, error)
		list    func(*gitv1.ListRequest) (*gitv1.ListResponse, error)
		want    string
	}{
		{name: "no remotes", want: "no supported"},
		{name: "unsupported host", remotes: []string{"https://user:secret@gitlab.com/acme/widgets"}, want: "no supported"},
		{name: "different remote org", remotes: []string{"https://test-org.code.depot.dev/widgets", "https://user:secret@other.code.depot.dev/widgets"}, want: "different organization"},
		{name: "missing repository", remotes: []string{"https://code.depot.dev/widgets"}, resolve: func(*gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
			return &gitv1.ResolveResponse{}, nil
		}, want: "no matching repository"},
		{name: "wrong response org", remotes: []string{"https://code.depot.dev/widgets"}, resolve: func(*gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
			return &gitv1.ResolveResponse{Repository: &gitv1.Repository{Id: "opaque-id", OrgId: "other"}}, nil
		}, want: "does not belong"},
		{name: "multiple native repositories", remotes: []string{"https://code.depot.dev/one", "https://code.depot.dev/two"}, resolve: func(req *gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
			return &gitv1.ResolveResponse{Repository: &gitv1.Repository{Id: "id-" + req.Repository.GetName(), OrgId: "test-org"}}, nil
		}, want: "multiple repositories"},
		{name: "no existing mirror", remotes: []string{"https://github.com/acme/widgets.git"}, list: func(*gitv1.ListRequest) (*gitv1.ListResponse, error) {
			return &gitv1.ListResponse{Repositories: []*gitv1.Repository{mirror("unrelated", "other/widgets")}}, nil
		}, want: "no existing Depot mirror"},
		{name: "ambiguous mirror on later page", remotes: []string{"https://github.com/acme/widgets.git"}, list: func(req *gitv1.ListRequest) (*gitv1.ListResponse, error) {
			if req.GetPageToken() == "" {
				return &gitv1.ListResponse{Repositories: []*gitv1.Repository{mirror("one", "acme/widgets")}, NextPageToken: proto.String("two")}, nil
			}
			return &gitv1.ListResponse{Repositories: []*gitv1.Repository{mirror("two", "acme/widgets")}}, nil
		}, want: "multiple repositories"},
		{name: "different GitHub remotes", remotes: []string{"https://github.com/acme/widgets.git", "git@github.com:other/widgets.git"}, list: func(*gitv1.ListRequest) (*gitv1.ListResponse, error) {
			return &gitv1.ListResponse{Repositories: []*gitv1.Repository{mirror("one", "acme/widgets"), mirror("two", "other/widgets")}}, nil
		}, want: "multiple repositories"},
		{name: "unrelated paired remotes", remotes: []string{"https://code.depot.dev/one", "git@github.com:other/widgets.git"}, resolve: func(*gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
			return &gitv1.ResolveResponse{Repository: mirror("one", "acme/widgets")}, nil
		}, list: func(*gitv1.ListRequest) (*gitv1.ListResponse, error) {
			return &gitv1.ListResponse{Repositories: []*gitv1.Repository{mirror("two", "other/widgets")}}, nil
		}, want: "multiple repositories"},
		{name: "wrong mirror org", remotes: []string{"git@github.com:acme/widgets.git"}, list: func(*gitv1.ListRequest) (*gitv1.ListResponse, error) {
			repo := mirror("one", "acme/widgets")
			repo.OrgId = "other"
			return &gitv1.ListResponse{Repositories: []*gitv1.Repository{repo}}, nil
		}, want: "does not belong"},
		{name: "listing failure with native target", remotes: []string{"https://code.depot.dev/widgets", "git@github.com:alice/widgets.git"}, resolve: func(*gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) {
			return &gitv1.ResolveResponse{Repository: mirror("one", "acme/widgets")}, nil
		}, list: func(*gitv1.ListRequest) (*gitv1.ListResponse, error) {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("listing failed"))
		}, want: "unavailable"},
		{name: "permission denied after partial mirror listing", remotes: []string{"git@github.com:acme/widgets.git"}, list: func(req *gitv1.ListRequest) (*gitv1.ListResponse, error) {
			if req.GetPageToken() == "" {
				return &gitv1.ListResponse{Repositories: []*gitv1.Repository{mirror("one", "acme/widgets")}, NextPageToken: proto.String("two")}, nil
			}
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("listing denied"))
		}, want: "permission_denied"},
		{name: "repeated pagination", remotes: []string{"git@github.com:acme/widgets.git"}, list: func(*gitv1.ListRequest) (*gitv1.ListResponse, error) {
			return &gitv1.ListResponse{NextPageToken: proto.String("same")}, nil
		}, want: "repeated page token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkout(t)
			for i, url := range tc.remotes {
				runGit(t, "remote", "add", fmt.Sprintf("remote%d", i), url)
			}
			mux := apiServer(t)
			mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("unexpected lookup or mutation") })
			if tc.resolve != nil {
				rpc(t, mux, gitv1connect.RepositoryServiceResolveProcedure, tc.resolve)
			}
			if tc.list != nil {
				rpc(t, mux, gitv1connect.RepositoryServiceListProcedure, tc.list)
			}
			out, err := executeInferred(t)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "--repo") || strings.Contains(err.Error(), "secret") || out != "" {
				t.Fatalf("expected safe refusal containing %q: %q %v", tc.want, out, err)
			}
		})
	}
}

func TestCreateLookupFailuresAreRedactedAndNeverMutate(t *testing.T) {
	for _, remote := range []string{"https://user:secret@code.depot.dev/widgets", "https://user:secret@github.com/acme/widgets.git"} {
		for _, status := range []connect.Code{connect.CodePermissionDenied, connect.CodeUnimplemented, connect.CodeUnavailable} {
			t.Run(remote+status.String(), func(t *testing.T) {
				checkout(t)
				runGit(t, "remote", "add", "origin", remote)
				mux := apiServer(t)
				mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("unexpected API request") })
				failure := connect.NewError(status, errors.New("could not resolve "+remote))
				rpc(t, mux, gitv1connect.RepositoryServiceResolveProcedure, func(*gitv1.ResolveRequest) (*gitv1.ResolveResponse, error) { return nil, failure })
				rpc(t, mux, gitv1connect.RepositoryServiceListProcedure, func(*gitv1.ListRequest) (*gitv1.ListResponse, error) { return nil, failure })
				out, err := executeInferred(t)
				if err == nil || !strings.Contains(err.Error(), status.String()) || !strings.Contains(err.Error(), "--repo") || strings.Contains(err.Error(), "secret") || out != "" {
					t.Fatalf("expected redacted refusal: %q %v", out, err)
				}
			})
		}
	}
}

func TestParseRepositoryRemoteRejectsLookalikesAndLocalPaths(t *testing.T) {
	for _, raw := range []string{
		"https://code.depot.dev.evil.test/widgets", "https://evilcode.depot.dev/widgets",
		"https://a.b.code.depot.dev/widgets", "https://.code.depot.dev/widgets",
		"https://github.com.evil.test/acme/widgets", "https://github.com/acme/widgets/extra",
		"/tmp/git@github.com:acme/widgets", "file://github.com/acme/widgets",
		"https://code.depot.dev/../widgets", "https://code.depot.dev/a//widgets",
		"ssh://git@code.depot.dev/widgets%0Asecret", "https://github.com/acme/.git",
	} {
		t.Run(raw, func(t *testing.T) {
			if remote, ok := parseRepositoryRemote(raw); ok {
				t.Fatalf("accepted invalid remote: %+v", remote)
			}
		})
	}
}
