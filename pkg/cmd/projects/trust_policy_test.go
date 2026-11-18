package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/spf13/viper"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type trustPolicyServer struct {
	corev1connect.UnimplementedProjectServiceHandler
	list     func(*corev1.ListTrustPoliciesRequest) (*corev1.ListTrustPoliciesResponse, error)
	add      func(*corev1.AddTrustPolicyRequest) (*corev1.AddTrustPolicyResponse, error)
	update   func(*corev1.UpdateTrustPolicyRequest) (*corev1.UpdateTrustPolicyResponse, error)
	remove   func(*corev1.RemoveTrustPolicyRequest) error
	requests atomic.Int32
}

func (s *trustPolicyServer) ListTrustPolicies(_ context.Context, req *connect.Request[corev1.ListTrustPoliciesRequest]) (*connect.Response[corev1.ListTrustPoliciesResponse], error) {
	if s.list == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("not supported"))
	}
	result, err := s.list(req.Msg)
	return connect.NewResponse(result), err
}
func (s *trustPolicyServer) AddTrustPolicy(_ context.Context, req *connect.Request[corev1.AddTrustPolicyRequest]) (*connect.Response[corev1.AddTrustPolicyResponse], error) {
	if s.add == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("not supported"))
	}
	result, err := s.add(req.Msg)
	return connect.NewResponse(result), err
}
func (s *trustPolicyServer) UpdateTrustPolicy(_ context.Context, req *connect.Request[corev1.UpdateTrustPolicyRequest]) (*connect.Response[corev1.UpdateTrustPolicyResponse], error) {
	if s.update == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("not supported"))
	}
	result, err := s.update(req.Msg)
	return connect.NewResponse(result), err
}
func (s *trustPolicyServer) RemoveTrustPolicy(_ context.Context, req *connect.Request[corev1.RemoveTrustPolicyRequest]) (*connect.Response[corev1.RemoveTrustPolicyResponse], error) {
	if s.remove == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("not supported"))
	}
	return connect.NewResponse(&corev1.RemoveTrustPolicyResponse{}), s.remove(req.Msg)
}

func setupTrustPolicyServer(t *testing.T, s *trustPolicyServer) {
	t.Helper()
	_, handler := corev1connect.NewProjectServiceHandler(s)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer org-fixture" {
			t.Errorf("unexpected authorization header %q", got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}), &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
}

func executeTrustPolicy(args ...string) (string, error) {
	cmd := NewCmdProjects()
	cmd.SetArgs(append([]string{"trust-policy"}, args...))
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	return output.String(), err
}

func gitlabPolicy(restricted bool) *corev1.TrustPolicy {
	return &corev1.TrustPolicy{TrustPolicyId: "policy-123", Provider: &corev1.TrustPolicy_Gitlab{Gitlab: &corev1.TrustPolicy_GitLab{NamespaceId: "123", ProjectId: "456", ProtectedRefsOnly: restricted}}}
}

func TestTrustPolicyAddProviders(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		want  *corev1.AddTrustPolicyRequest
		saved *corev1.TrustPolicy
	}{
		{"gitlab", []string{"--namespace-id", "123", "--gitlab-project-id", "456", "--protected-refs-only"}, &corev1.AddTrustPolicyRequest{Provider: &corev1.AddTrustPolicyRequest_Gitlab{Gitlab: gitlabPolicy(true).GetGitlab()}}, gitlabPolicy(true)},
		{"github", []string{"--repository-owner", "acme", "--repository", "app", "--allow-public-pull-requests"}, &corev1.AddTrustPolicyRequest{Provider: &corev1.AddTrustPolicyRequest_Github{Github: &corev1.TrustPolicy_GitHub{RepositoryOwner: "acme", Repository: "app", AllowPublicPullRequests: true}}}, &corev1.TrustPolicy{TrustPolicyId: "gh-policy", Provider: &corev1.TrustPolicy_Github{Github: &corev1.TrustPolicy_GitHub{RepositoryOwner: "acme", Repository: "app", AllowPublicPullRequests: true}}}},
		{"circleci", []string{"--organization-uuid", "00000000-0000-0000-0000-000000000001", "--project-uuid", "00000000-0000-0000-0000-000000000002"}, &corev1.AddTrustPolicyRequest{Provider: &corev1.AddTrustPolicyRequest_Circleci{Circleci: &corev1.TrustPolicy_CircleCI{OrganizationUuid: "00000000-0000-0000-0000-000000000001", ProjectUuid: "00000000-0000-0000-0000-000000000002"}}}, &corev1.TrustPolicy{TrustPolicyId: "circle-policy", Provider: &corev1.TrustPolicy_Circleci{Circleci: &corev1.TrustPolicy_CircleCI{OrganizationUuid: "00000000-0000-0000-0000-000000000001", ProjectUuid: "00000000-0000-0000-0000-000000000002"}}}},
		{"buildkite", []string{"--organization-slug", "acme", "--pipeline-slug", "app"}, &corev1.AddTrustPolicyRequest{Provider: &corev1.AddTrustPolicyRequest_Buildkite{Buildkite: &corev1.TrustPolicy_Buildkite{OrganizationSlug: "acme", PipelineSlug: "app"}}}, &corev1.TrustPolicy{TrustPolicyId: "bk-policy", Provider: &corev1.TrustPolicy_Buildkite_{Buildkite: &corev1.TrustPolicy_Buildkite{OrganizationSlug: "acme", PipelineSlug: "app"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.want.ProjectId = "depot-project"
			s := &trustPolicyServer{add: func(req *corev1.AddTrustPolicyRequest) (*corev1.AddTrustPolicyResponse, error) {
				if !proto.Equal(req, tc.want) {
					t.Errorf("wire request = %v, want %v", req, tc.want)
				}
				return &corev1.AddTrustPolicyResponse{TrustPolicy: tc.saved}, nil
			}}
			setupTrustPolicyServer(t, s)
			args := append([]string{"add", "--provider", tc.name, "--project-id", "depot-project", "--token", "org-fixture", "--output", "json"}, tc.args...)
			output, err := executeTrustPolicy(args...)
			if err != nil {
				t.Fatal(err)
			}
			var got corev1.TrustPolicy
			if err := protojson.Unmarshal([]byte(output), &got); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(&got, tc.saved) {
				t.Fatalf("output = %s", output)
			}
			if s.requests.Load() != 1 {
				t.Fatalf("requests = %d", s.requests.Load())
			}
		})
	}
}

func TestTrustPolicyAddShowsExistingRestrictions(t *testing.T) {
	s := &trustPolicyServer{add: func(req *corev1.AddTrustPolicyRequest) (*corev1.AddTrustPolicyResponse, error) {
		if req.GetGitlab().GetProtectedRefsOnly() {
			t.Error("omitted flag must not enable protection")
		}
		return &corev1.AddTrustPolicyResponse{TrustPolicy: gitlabPolicy(true)}, nil
	}}
	setupTrustPolicyServer(t, s)
	output, err := executeTrustPolicy("add", "--provider", "gitlab", "--namespace-id", "123", "--gitlab-project-id", "456", "-p", "depot-project", "--token", "org-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "policy-123") || !strings.Contains(output, "protected-refs-only=true") {
		t.Fatalf("did not show stored restrictions: %s", output)
	}
	if s.requests.Load() != 1 {
		t.Fatal("add must not update an existing policy")
	}
}

func TestTrustPolicyUpdatePreservesPresenceAcrossHTTP(t *testing.T) {
	for _, flag := range []string{"protected-refs-only", "allow-public-pull-requests"} {
		for _, value := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%t", flag, value), func(t *testing.T) {
				var saved *corev1.TrustPolicy
				s := &trustPolicyServer{update: func(req *corev1.UpdateTrustPolicyRequest) (*corev1.UpdateTrustPolicyResponse, error) {
					if req.ProjectId != "depot-project" || req.TrustPolicyId != "policy-123" {
						t.Errorf("wrong target: %v", req)
					}
					if flag == "protected-refs-only" {
						if req.ProtectedRefsOnly == nil || *req.ProtectedRefsOnly != value || req.AllowPublicPullRequests != nil {
							t.Errorf("lost explicit boolean or sent unrelated field: %v", req)
						}
						saved = gitlabPolicy(value)
					} else {
						if req.AllowPublicPullRequests == nil || *req.AllowPublicPullRequests != value || req.ProtectedRefsOnly != nil {
							t.Errorf("lost explicit boolean or sent unrelated field: %v", req)
						}
						saved = &corev1.TrustPolicy{TrustPolicyId: "policy-123", Provider: &corev1.TrustPolicy_Github{Github: &corev1.TrustPolicy_GitHub{RepositoryOwner: "acme", Repository: "app", AllowPublicPullRequests: value}}}
					}
					return &corev1.UpdateTrustPolicyResponse{TrustPolicy: saved}, nil
				}}
				setupTrustPolicyServer(t, s)
				output, err := executeTrustPolicy("update", "policy-123", "-p", "depot-project", "--token", "org-fixture", fmt.Sprintf("--%s=%t", flag, value), "-o", "json")
				if err != nil {
					t.Fatal(err)
				}
				var got corev1.TrustPolicy
				if err := protojson.Unmarshal([]byte(output), &got); err != nil {
					t.Fatal(err)
				}
				if !proto.Equal(&got, saved) {
					t.Fatalf("output = %s", output)
				}
				if !value && !strings.Contains(output, "false") {
					t.Fatalf("JSON hides disabled restriction: %s", output)
				}
			})
		}
	}
}

func TestTrustPolicyListPaginationAndOutput(t *testing.T) {
	for _, outputFormat := range []string{"", "json"} {
		t.Run(outputFormat, func(t *testing.T) {
			s := &trustPolicyServer{list: func(req *corev1.ListTrustPoliciesRequest) (*corev1.ListTrustPoliciesResponse, error) {
				if req.ProjectId != "depot-project" || req.GetProvider() != corev1.TrustPolicyProvider_TRUST_POLICY_PROVIDER_GITLAB || req.GetPageSize() != 1 || req.GetPageToken() != "page-2" {
					t.Errorf("wrong page/filter: %v", req)
				}
				return &corev1.ListTrustPoliciesResponse{TrustPolicies: []*corev1.TrustPolicy{gitlabPolicy(false)}, NextPageToken: proto.String("page-3")}, nil
			}}
			setupTrustPolicyServer(t, s)
			output, err := executeTrustPolicy("list", "-p", "depot-project", "--provider", "gitlab", "--page-size", "1", "--page-token", "page-2", "--token", "org-fixture", "--output", outputFormat)
			if err != nil {
				t.Fatal(err)
			}
			if outputFormat == "json" {
				var got corev1.ListTrustPoliciesResponse
				if err := protojson.Unmarshal([]byte(output), &got); err != nil {
					t.Fatal(err)
				}
				if got.GetNextPageToken() != "page-3" || len(got.TrustPolicies) != 1 || !proto.Equal(got.TrustPolicies[0], gitlabPolicy(false)) {
					t.Fatalf("wrong page: %s", output)
				}
				var view struct {
					Policies []struct {
						Gitlab map[string]any `json:"gitlab"`
					} `json:"trust_policies"`
				}
				if err := json.Unmarshal([]byte(output), &view); err != nil {
					t.Fatal(err)
				}
				if len(view.Policies) != 1 || view.Policies[0].Gitlab["protected_refs_only"] != false {
					t.Fatalf("JSON hides false: %s", output)
				}
			} else if !strings.Contains(output, "protected-refs-only=false") || !strings.Contains(output, "Next page token: page-3") {
				t.Fatalf("incomplete output: %s", output)
			}
		})
	}
}

func TestTrustPolicyEmptyList(t *testing.T) {
	s := &trustPolicyServer{list: func(req *corev1.ListTrustPoliciesRequest) (*corev1.ListTrustPoliciesResponse, error) {
		if req.Provider != nil {
			t.Error("unfiltered list must omit provider")
		}
		return &corev1.ListTrustPoliciesResponse{}, nil
	}}
	setupTrustPolicyServer(t, s)
	output, err := executeTrustPolicy("list", "-p", "depot-project", "--token", "org-fixture", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatal(err)
	}
	if rows, ok := got["trust_policies"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("want empty array, got %s", output)
	}
}

func TestTrustPolicyRemoveScope(t *testing.T) {
	s := &trustPolicyServer{remove: func(req *corev1.RemoveTrustPolicyRequest) error {
		if req.ProjectId != "depot-project" || req.TrustPolicyId != "policy-123" {
			t.Errorf("wrong remove target: %v", req)
		}
		return nil
	}}
	setupTrustPolicyServer(t, s)
	output, err := executeTrustPolicy("remove", "policy-123", "-p", "depot-project", "--token", "org-fixture", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output) != "{}" || s.requests.Load() != 1 {
		t.Fatalf("remove result = %q, requests = %d", output, s.requests.Load())
	}
}

func TestTrustPolicyValidationBeforeNetwork(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"list", "--provider", "unknown"}, "unsupported provider"},
		{[]string{"list", "--provider="}, "unsupported provider"},
		{[]string{"list", "--page-size", "0"}, "--page-size"},
		{[]string{"list", "--page-size", "251"}, "--page-size"},
		{[]string{"list", "--output", "yaml"}, "unsupported output"},
		{[]string{"add"}, "unsupported provider"},
		{[]string{"add", "--provider", "gitlab"}, "--namespace-id is required"},
		{[]string{"add", "--provider", "gitlab", "--namespace-id", "123"}, "--gitlab-project-id is required"},
		{[]string{"add", "--provider", "github", "--repository-owner", "acme", "--repository", "  "}, "--repository is required"},
		{[]string{"add", "--provider", "github", "--protected-refs-only=false"}, "only valid with --provider gitlab"},
		{[]string{"add", "--provider", "gitlab", "--allow-public-pull-requests=false"}, "only valid with --provider github"},
		{[]string{"add", "--provider", "buildkite", "--organization-uuid", "x"}, "only valid with --provider circleci"},
		{[]string{"update", "policy-123"}, "is required"},
		{[]string{"update", "policy-123", "--protected-refs-only=false", "--allow-public-pull-requests=false"}, "none of the others can be"},
		{[]string{"update", " ", "--protected-refs-only"}, "must not be empty"},
		{[]string{"remove", " "}, "must not be empty"},
		{[]string{"remove"}, "accepts 1 arg"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			s := &trustPolicyServer{}
			setupTrustPolicyServer(t, s)
			output, err := executeTrustPolicy(append(tc.args, "-p", "depot-project")...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q error, got %v", tc.want, err)
			}
			if output != "" || s.requests.Load() != 0 {
				t.Fatalf("invalid input emitted output or reached API: %q, %d requests", output, s.requests.Load())
			}
		})
	}
}

func TestTrustPolicyErrorsDoNotReportSuccessOrFallback(t *testing.T) {
	for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeUnauthenticated, connect.CodeNotFound, connect.CodeInvalidArgument, connect.CodeUnavailable, connect.CodeUnimplemented} {
		for _, operation := range []string{"list", "add", "update", "remove"} {
			t.Run(operation+"/"+code.String(), func(t *testing.T) {
				apiErr := connect.NewError(code, fmt.Errorf("server rejected request"))
				s := &trustPolicyServer{
					list:   func(*corev1.ListTrustPoliciesRequest) (*corev1.ListTrustPoliciesResponse, error) { return nil, apiErr },
					add:    func(*corev1.AddTrustPolicyRequest) (*corev1.AddTrustPolicyResponse, error) { return nil, apiErr },
					update: func(*corev1.UpdateTrustPolicyRequest) (*corev1.UpdateTrustPolicyResponse, error) { return nil, apiErr },
					remove: func(*corev1.RemoveTrustPolicyRequest) error { return apiErr },
				}
				setupTrustPolicyServer(t, s)
				args := []string{operation, "-p", "depot-project", "--token", "org-fixture"}
				switch operation {
				case "add":
					args = append(args, "--provider", "gitlab", "--namespace-id", "123", "--gitlab-project-id", "456", "--protected-refs-only")
				case "update":
					args = append(args, "policy-123", "--protected-refs-only=false")
				case "remove":
					args = append(args, "policy-123")
				}
				output, err := executeTrustPolicy(args...)
				if connect.CodeOf(err) != code {
					t.Fatalf("error = %v, want %s", err, code)
				}
				if code == connect.CodeUnimplemented && !strings.Contains(err.Error(), "API deployment") {
					t.Fatalf("missing compatibility guidance: %v", err)
				}
				if output != "" || s.requests.Load() != 1 {
					t.Fatalf("unexpected success output or fallback: %q, %d requests", output, s.requests.Load())
				}
			})
		}
	}
}

func TestTrustPolicyProjectAndCredentialResolution(t *testing.T) {
	previous := viper.GetString("api_token")
	t.Cleanup(func() { viper.Set("api_token", previous) })
	for _, key := range []string{"DEPOT_TOKEN", "DEPOT_API_TOKEN", "DEPOT_PROJECT_ID", "DEPOT_JIT_TOKEN", "DEPOT_CACHE_TOKEN", "DEPOT_RUN_TOKEN_URL"} {
		t.Setenv(key, "")
	}
	for _, source := range []string{"flags", "environment", "config", "missing token", "missing project"} {
		t.Run(source, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("DEPOT_TOKEN", "")
			t.Setenv("DEPOT_PROJECT_ID", "")
			viper.Set("api_token", "")
			args := []string{"list"}
			switch source {
			case "flags":
				args = append(args, "-p", "depot-project", "--token", "org-fixture")
				t.Setenv("DEPOT_TOKEN", "wrong-env-token")
				t.Setenv("DEPOT_PROJECT_ID", "wrong-env-project")
			case "environment":
				t.Setenv("DEPOT_TOKEN", "org-fixture")
				t.Setenv("DEPOT_PROJECT_ID", "depot-project")
				viper.Set("api_token", "wrong-config-token")
			case "config":
				viper.Set("api_token", "org-fixture")
				if err := os.WriteFile(filepath.Join(".", "depot.json"), []byte(`{"id":"depot-project"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing token":
				args = append(args, "-p", "depot-project")
			case "missing project":
				args = append(args, "--token", "org-fixture")
			}
			s := &trustPolicyServer{list: func(req *corev1.ListTrustPoliciesRequest) (*corev1.ListTrustPoliciesResponse, error) {
				if req.ProjectId != "depot-project" {
					t.Errorf("wrong resolved project: %s", req.ProjectId)
				}
				return &corev1.ListTrustPoliciesResponse{}, nil
			}}
			setupTrustPolicyServer(t, s)
			_, err := executeTrustPolicy(args...)
			if strings.HasPrefix(source, "missing") {
				if err == nil || s.requests.Load() != 0 {
					t.Fatalf("missing input must fail before API: %v, %d requests", err, s.requests.Load())
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTrustPolicyTextEscapesControlCharacters(t *testing.T) {
	policy := &corev1.TrustPolicy{TrustPolicyId: "id\x1b[2J\nforged", Provider: &corev1.TrustPolicy_Github{Github: &corev1.TrustPolicy_GitHub{RepositoryOwner: "acme\towner", Repository: "app\u202eforged"}}}
	s := &trustPolicyServer{list: func(*corev1.ListTrustPoliciesRequest) (*corev1.ListTrustPoliciesResponse, error) {
		return &corev1.ListTrustPoliciesResponse{TrustPolicies: []*corev1.TrustPolicy{policy}, NextPageToken: proto.String("next\x1b[2J")}, nil
	}}
	setupTrustPolicyServer(t, s)
	output, err := executeTrustPolicy("list", "-p", "depot-project", "--token", "org-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(output, "\x1b\u202e\t") || strings.Contains(output, "\nforged") {
		t.Fatalf("unsafe terminal output %q", output)
	}
}
