package code

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	gitv1 "github.com/depot/cli/pkg/proto/depot/git/v1"
	"github.com/depot/cli/pkg/proto/depot/git/v1/gitv1connect"
	"github.com/spf13/viper"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func createServer(t *testing.T, handler func(*connect.Request[gitv1.CreateRequest]) (*gitv1.CreateResponse, error)) {
	t.Helper()
	server := httptest.NewServer(h2c.NewHandler(connect.NewUnaryHandler(
		gitv1connect.RepositoryServiceCreateProcedure,
		func(_ context.Context, req *connect.Request[gitv1.CreateRequest]) (*connect.Response[gitv1.CreateResponse], error) {
			if !strings.HasPrefix(req.Header().Get("User-Agent"), "depot-cli/") {
				t.Error("missing CLI user agent")
			}
			response, err := handler(req)
			if err != nil {
				return nil, err
			}
			return connect.NewResponse(response), nil
		}), &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
}

func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewCmdCode()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestCreateRepository(t *testing.T) {
	for _, tc := range []struct{ name, branch, output string }{
		{"widgets", "main", ""},
		{"team/service", "develop", "json"},
		{"team/café", "main", "json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			createServer(t, func(req *connect.Request[gitv1.CreateRequest]) (*gitv1.CreateResponse, error) {
				calls++
				if req.Header().Get("Authorization") != "Bearer test-token" || req.Header().Get("X-Depot-Org") != "test-org" {
					t.Error("incorrect credential or organization")
				}
				if req.Msg.CanonicalName != tc.name || req.Msg.DefaultBranch != tc.branch || req.Msg.ObjectFormat != gitv1.ObjectFormat_OBJECT_FORMAT_SHA1 || req.Msg.Upstream != nil {
					t.Errorf("unexpected standalone creation request: %v", req.Msg)
				}
				return &gitv1.CreateResponse{Id: "repo-id"}, nil
			})
			args := []string{"repo", "create", tc.name, "--org", "test-org", "--token", "test-token"}
			if tc.branch != "main" {
				args = append(args, "--default-branch", tc.branch)
			}
			if tc.output != "" {
				args = append(args, "--output", tc.output)
			}
			out, err := execute(t, args...)
			if err != nil || calls != 1 {
				t.Fatalf("create: output=%q err=%v calls=%d", out, err, calls)
			}
			if strings.Contains(out, "test-token") {
				t.Fatal("output leaked credentials")
			}
			if tc.output == "json" {
				var result map[string]string
				if err := json.Unmarshal([]byte(out), &result); err != nil {
					t.Fatal(err)
				}
				wantURL := "https://test-org.code.depot.dev/" + strings.ReplaceAll(tc.name, "é", "%C3%A9")
				if len(result) != 4 || result["repositoryId"] != "repo-id" || result["name"] != tc.name || result["defaultBranch"] != tc.branch || result["cloneUrl"] != wantURL {
					t.Fatalf("unexpected JSON result: %v", result)
				}
			} else if out != "Created widgets (repo-id)\nClone URL: https://test-org.code.depot.dev/widgets\n" {
				t.Fatalf("unexpected text output: %q", out)
			}
		})
	}
}

func TestCreateValidationDoesNotSendRequest(t *testing.T) {
	createServer(t, func(_ *connect.Request[gitv1.CreateRequest]) (*gitv1.CreateResponse, error) {
		t.Error("invalid arguments reached the server")
		return nil, connect.NewError(connect.CodeInternal, errors.New("unexpected request"))
	})
	for _, args := range [][]string{
		{"repo", "create"}, {"repo", "create", "a", "b"}, {"repo", "create", " "},
		{"repo", "create", "repo", "--default-branch", ""},
		{"repo", "create", "repo", "--output", "yaml"},
		{"repo", "create", "repo", "--org", ""},
	} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			out, err := execute(t, append(args, "--token", "test-token")...)
			if err == nil || out != "" {
				t.Fatalf("expected error without output, got %q %v", out, err)
			}
		})
	}
}

func TestCreateAuthenticationAndCurrentOrganization(t *testing.T) {
	oldOrg, oldToken := viper.Get("org_id"), viper.Get("api_token")
	t.Cleanup(func() { viper.Set("org_id", oldOrg); viper.Set("api_token", oldToken) })
	viper.Set("org_id", "current-org")
	viper.Set("api_token", "login-token")
	for _, tc := range []struct{ flag, env, want string }{
		{"flag-token", "env-token", "flag-token"},
		{"", "env-token", "env-token"},
		{"", "", "login-token"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Setenv("DEPOT_TOKEN", tc.env)
			createServer(t, func(req *connect.Request[gitv1.CreateRequest]) (*gitv1.CreateResponse, error) {
				if req.Header().Get("Authorization") != "Bearer "+tc.want || req.Header().Get("X-Depot-Org") != "current-org" {
					t.Error("incorrect resolved identity")
				}
				return &gitv1.CreateResponse{Id: "created"}, nil
			})
			args := []string{"repo", "create", "sample"}
			if tc.flag != "" {
				args = append(args, "--token", tc.flag)
			}
			out, err := execute(t, args...)
			if err != nil || !strings.Contains(out, "https://current-org.code.depot.dev/sample") {
				t.Fatalf("create: %q %v", out, err)
			}
		})
	}
}

func TestCreateServerRejectionsAreNotRetried(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeInvalidArgument, connect.CodeAlreadyExists, connect.CodePermissionDenied, connect.CodeUnauthenticated, connect.CodeUnavailable, connect.CodeDeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			calls := 0
			createServer(t, func(_ *connect.Request[gitv1.CreateRequest]) (*gitv1.CreateResponse, error) {
				calls++
				return nil, connect.NewError(code, errors.New("server rejection"))
			})
			out, err := execute(t, "repo", "create", "sample", "--org", "test-org", "--token", "test-token", "--output", "json")
			if connect.CodeOf(err) != code || calls != 1 || out != "" {
				t.Fatalf("create: output=%q error=%v calls=%d", out, err, calls)
			}
		})
	}
}

func TestCreateRejectsMissingRepositoryID(t *testing.T) {
	createServer(t, func(_ *connect.Request[gitv1.CreateRequest]) (*gitv1.CreateResponse, error) {
		return &gitv1.CreateResponse{}, nil
	})
	out, err := execute(t, "repo", "create", "sample", "--org", "test-org", "--token", "test-token")
	if err == nil || !strings.Contains(err.Error(), "check whether it was created") || out != "" {
		t.Fatalf("create: %q %v", out, err)
	}
}
