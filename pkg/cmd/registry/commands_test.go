package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	v1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/registry/v1beta1"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func execute(args []string, stdin string) (string, error) {
	cmd := NewCmdRegistry()
	cmd.SetArgs(args)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(stdin))
	err := cmd.Execute()
	return out.String(), err
}

// Decode actual Connect protobuf requests and return protobuf responses. This
// exercises Cobra parsing, authentication, optional-field presence, and output.
func endpoint(t *testing.T, method string, want proto.Message, response proto.Message, status int, body string) *atomic.Int32 {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/depot.registry.v1beta1.RegistryService/"+method {
			t.Errorf("unexpected RPC: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer org-test" || r.Header.Get("X-Depot-Org") != "" {
			t.Error("incorrect authentication/tenant scope")
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		got := want.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(data, got); err != nil {
			t.Error(err)
			return
		}
		if !proto.Equal(got, want) {
			t.Errorf("request = %v, want %v", got, want)
		}
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
		data, err = proto.Marshal(response)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/proto")
		_, _ = w.Write(data)
	}), &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	t.Setenv("DEPOT_TOKEN", "org-test")
	return calls
}

func TestRegistryOperations(t *testing.T) {
	for _, tc := range []struct {
		name, method    string
		args            []string
		input, response proto.Message
	}{
		{"repository list", "ListRepositories", []string{"repository", "list", "--page", "2", "--page-size", "10", "--query", "team"}, &v1.ListRepositoriesRequest{Page: 2, PageSize: 10, Query: proto.String("team")}, &v1.ListRepositoriesResponse{Page: 2, PageSize: 10, HasMore: true, Repositories: []*v1.RepositorySummary{{Name: "team/app", SizeBytes: 9007199254740993}}}},
		{"repository create", "CreateRepository", []string{"repository", "create", "team/app"}, &v1.CreateRepositoryRequest{Repository: "team/app"}, &v1.CreateRepositoryResponse{}},
		{"pull through create", "CreateRepository", []string{"repository", "create", "team/app", "--upstream-id", "up-1", "--upstream-repository", "library/alpine"}, &v1.CreateRepositoryRequest{Repository: "team/app", Upstream: &v1.RepositoryUpstream{UpstreamRegistryId: "up-1", UpstreamRepository: "library/alpine"}}, &v1.CreateRepositoryResponse{UpstreamRepository: &v1.UpstreamRepository{Id: "link-1", Repository: "team/app", UpstreamRegistryId: "up-1", UpstreamRepository: "library/alpine"}}},
		{"repository delete", "DeleteRepository", []string{"repository", "delete", "team/app", "--yes"}, &v1.DeleteRepositoryRequest{Repository: "team/app"}, &v1.DeleteRepositoryResponse{}},
		{"images filtered page", "ListImages", []string{"image", "list", "team/app", "--page", "2", "--page-size", "1", "--query", "release", "--tag-status", "tagged"}, &v1.ListImagesRequest{Repository: "team/app", Page: 2, PageSize: 1, TagQuery: proto.String("release"), TagStatus: v1.ImageTagStatus_IMAGE_TAG_STATUS_TAGGED}, &v1.ListImagesResponse{Page: 2, PageSize: 1, HasMore: true, Images: []*v1.ImageSummary{{Digest: "sha256:fixture", Tags: []string{"release"}}}}},
		{"inspect", "GetImageDetail", []string{"image", "inspect", "team/app", "latest"}, &v1.GetImageDetailRequest{Repository: "team/app", Reference: "latest"}, &v1.GetImageDetailResponse{Digest: "sha256:fixture", Manifest: []byte(`{"schemaVersion":2}`)}},
		{"image delete", "BatchDeleteImage", []string{"image", "delete", "team/app", "--tag", "one", "--tag", "two", "--yes"}, &v1.BatchDeleteImageRequest{Repository: "team/app", ImageIds: []*v1.ImageIdentifier{{ImageTag: proto.String("one")}, {ImageTag: proto.String("two")}}}, &v1.BatchDeleteImageResponse{ImageIds: []*v1.ImageIdentifier{{ImageTag: proto.String("one")}, {ImageTag: proto.String("two")}}}},
		{"tag delete", "DeleteTag", []string{"tag", "delete", "team/app", "latest", "--yes"}, &v1.DeleteTagRequest{Repository: "team/app", Tag: "latest"}, &v1.DeleteTagResponse{}},
		{"token list", "ListTokens", []string{"token", "list"}, &v1.ListTokensRequest{Page: 1, PageSize: 25}, &v1.ListTokensResponse{Page: 1, PageSize: 25, TotalCount: 1, Tokens: []*v1.RegistryAccessToken{{Id: "token-1", Name: "test", Permission: v1.RegistryAccessTokenPermission_REGISTRY_ACCESS_TOKEN_PERMISSION_PULL, RepositoryScopes: []string{"team/*"}}}}},
		{"token create", "CreateToken", []string{"token", "create", "test", "--permission", "pull", "--scope", "team/*", "--description", "CI"}, &v1.CreateTokenRequest{Name: "test", Permission: v1.RegistryAccessTokenPermission_REGISTRY_ACCESS_TOKEN_PERMISSION_PULL, RepositoryScopes: []string{"team/*"}, Description: proto.String("CI")}, &v1.CreateTokenResponse{Token: &v1.RegistryAccessToken{Id: "token-1"}, Secret: "one-time-secret"}},
		{"token revoke", "RevokeToken", []string{"token", "revoke", "token-1", "--yes"}, &v1.RevokeTokenRequest{TokenId: "token-1"}, &v1.RevokeTokenResponse{Token: &v1.RegistryAccessToken{Id: "token-1"}}},
		{"missing retention", "GetRetentionPolicy", []string{"retention", "get", "team/app"}, &v1.GetRetentionPolicyRequest{Repository: "team/app"}, &v1.GetRetentionPolicyResponse{}},
		{"retention count", "UpsertRetentionPolicy", []string{"retention", "set", "team/app", "--keep-count", "10", "--yes"}, &v1.UpsertRetentionPolicyRequest{Repository: "team/app", Enabled: true, KeepCount: proto.Int32(10)}, &v1.UpsertRetentionPolicyResponse{Policy: &v1.RetentionPolicy{Repository: "team/app", Enabled: true, KeepCount: proto.Int32(10)}}},
		{"retention days", "UpsertRetentionPolicy", []string{"retention", "set", "**", "--keep-days", "30", "--yes"}, &v1.UpsertRetentionPolicyRequest{Repository: "**", Enabled: true, KeepDays: proto.Int32(30)}, &v1.UpsertRetentionPolicyResponse{Policy: &v1.RetentionPolicy{Repository: "**", Enabled: true, KeepDays: proto.Int32(30)}}},
		{"retention delete", "DeleteRetentionPolicy", []string{"retention", "delete", "team/app", "--yes"}, &v1.DeleteRetentionPolicyRequest{Repository: "team/app"}, &v1.DeleteRetentionPolicyResponse{}},
		{"upstreams", "ListUpstreamRegistries", []string{"upstream", "list"}, &v1.ListUpstreamRegistriesRequest{}, &v1.ListUpstreamRegistriesResponse{UpstreamRegistries: []*v1.UpstreamRegistry{{Id: "up-1", HasPassword: true}}}},
		{"upstream create", "CreateUpstreamRegistry", []string{"upstream", "create", "test", "--url", "https://registry.example.com", "--username", "user", "--password-stdin"}, &v1.CreateUpstreamRegistryRequest{Name: "test", UpstreamUrl: "https://registry.example.com", Username: "user", Password: "stdin-secret", AuthType: v1.UpstreamRegistryAuthType_UPSTREAM_REGISTRY_AUTH_TYPE_BASIC}, &v1.CreateUpstreamRegistryResponse{UpstreamRegistry: &v1.UpstreamRegistry{Id: "up-1", HasPassword: true}}},
		{"upstream ecr", "CreateUpstreamRegistry", []string{"upstream", "create", "ecr", "--url", "https://123.dkr.ecr.us-east-1.amazonaws.com", "--auth-type", "ecr", "--role-arn", "arn:aws:iam::123:role/test"}, &v1.CreateUpstreamRegistryRequest{Name: "ecr", UpstreamUrl: "https://123.dkr.ecr.us-east-1.amazonaws.com", AuthType: v1.UpstreamRegistryAuthType_UPSTREAM_REGISTRY_AUTH_TYPE_ECR, RoleArn: proto.String("arn:aws:iam::123:role/test")}, &v1.CreateUpstreamRegistryResponse{UpstreamRegistry: &v1.UpstreamRegistry{Id: "up-1"}}},
		{"upstream omitted password", "UpdateUpstreamRegistry", []string{"upstream", "update", "up-1", "--username="}, &v1.UpdateUpstreamRegistryRequest{UpstreamRegistryId: "up-1", Username: proto.String("")}, &v1.UpdateUpstreamRegistryResponse{UpstreamRegistry: &v1.UpstreamRegistry{Id: "up-1", HasPassword: true}}},
		{"upstream password", "UpdateUpstreamRegistry", []string{"upstream", "update", "up-1", "--password-stdin"}, &v1.UpdateUpstreamRegistryRequest{UpstreamRegistryId: "up-1", Password: proto.String("stdin-secret")}, &v1.UpdateUpstreamRegistryResponse{UpstreamRegistry: &v1.UpstreamRegistry{Id: "up-1", HasPassword: true}}},
		{"upstream delete", "DeleteUpstreamRegistry", []string{"upstream", "delete", "up-1", "--yes"}, &v1.DeleteUpstreamRegistryRequest{UpstreamRegistryId: "up-1"}, &v1.DeleteUpstreamRegistryResponse{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := endpoint(t, tc.method, tc.input, tc.response, 200, "")
			out, err := execute(tc.args, "stdin-secret\r\n")
			if err != nil {
				t.Fatal(err)
			}
			got := tc.response.ProtoReflect().New().Interface()
			if err := protojson.Unmarshal([]byte(out), got); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(got, tc.response) {
				t.Fatalf("output = %s", out)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d", calls.Load())
			}
			if strings.Contains(out, "stdin-secret") || strings.Contains(out, "org-test") {
				t.Fatal("credential leaked")
			}
			if tc.method == "CreateToken" && strings.Count(out, "one-time-secret") != 1 {
				t.Fatal("missing one-time secret")
			}
		})
	}
}

func TestRegistryValidationBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }), &http2.Server{}))
	defer server.Close()
	t.Setenv("DEPOT_API_URL", server.URL)
	t.Setenv("DEPOT_TOKEN", "org-test")
	for _, args := range [][]string{
		{"repository", "list", "--page", "0"}, {"repository", "list", "--page-size", "101"}, {"repository", "list", "--output", "yaml"},
		{"repository", "create", "**"}, {"repository", "create", "https://registry.example.com/team/app"}, {"repository", "create", "team/app", "--upstream-id", "up-1"},
		{"repository", "delete", "team/app"}, {"image", "delete", "team/app", "--yes"}, {"image", "delete", "team/app", "--digest", "oops", "--yes"},
		{"image", "list", "team/app", "--tag-status", "wrong"}, {"tag", "delete", "team/app", "latest"},
		{"token", "create", "test", "--scope", "team/*"}, {"token", "create", "test", "--permission", "push"}, {"token", "create", "test", "--permission", "pull", "--scope", "team/foo*"},
		{"token", "create", "test", "--permission", "pull", "--scope", "**", "--expires-at", "2020-01-01T00:00:00Z"}, {"token", "revoke", "id"},
		{"retention", "set", "team/app", "--keep-count", "1"}, {"retention", "set", "team/app", "--keep-count", "0", "--yes"}, {"retention", "set", "team/app", "--keep-count", "1", "--keep-days", "1", "--yes"}, {"retention", "set", "team/app", "--yes"}, {"retention", "delete", "**"},
		{"upstream", "create", "name", "--url", "https://user:secret@example.com"},
		{"upstream", "update", "id", "--url", "user:secret@example.com"},
		{"upstream", "create", "name"}, {"upstream", "create", "name", "--url", "example.com", "--auth-type", "ecr"}, {"upstream", "update", "id"}, {"upstream", "delete", "id"},
		{"upstream", "create", "name", "--url", "example.com", "--password", "secret"},
		{"repository", "list", "--organization", "other-org"}, {"repository", "create", " "},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := execute(args, "")
			if err == nil {
				t.Fatal("invalid input accepted")
			}
			if out != "" {
				t.Fatalf("unexpected output %q", out)
			}
		})
	}
	t.Setenv("DEPOT_TOKEN", "")
	if _, err := execute([]string{"repository", "list"}, ""); err == nil {
		t.Fatal("missing token accepted")
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached API: %d", calls.Load())
	}
}

func TestRegistryFailuresAreSafeAndDoNotRetry(t *testing.T) {
	for _, status := range []int{403, 501} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			code := "permission_denied"
			if status == 501 {
				code = "unimplemented"
			}
			req := &v1.UpdateUpstreamRegistryRequest{UpstreamRegistryId: "up-1", Password: proto.String("stdin-secret")}
			calls := endpoint(t, "UpdateUpstreamRegistry", req, nil, status, `{"code":"`+code+`","message":"org-test stdin-secret private details"}`)
			out, err := execute([]string{"upstream", "update", "up-1", "--password-stdin"}, "stdin-secret")
			if err == nil || err.Error() != "registry operation failed ("+code+")" || out != "" || calls.Load() != 1 {
				t.Fatalf("out=%q error=%v calls=%d", out, err, calls.Load())
			}
		})
	}
}
func TestRegistryPartialDeletionFailsWithResults(t *testing.T) {
	req := &v1.BatchDeleteImageRequest{Repository: "team/app", ImageIds: []*v1.ImageIdentifier{{ImageTag: proto.String("missing")}}}
	response := &v1.BatchDeleteImageResponse{Failures: []*v1.ImageFailure{{ImageId: req.ImageIds[0], FailureCode: "ImageNotFound", FailureReason: "No image"}}}
	endpoint(t, "BatchDeleteImage", req, response, 200, "")
	out, err := execute([]string{"image", "delete", "team/app", "--tag", "missing", "--yes"}, "")
	if err == nil {
		t.Fatal("partial failure reported success")
	}
	var data map[string]any
	if json.Unmarshal([]byte(out), &data) != nil || len(data["failures"].([]any)) != 1 {
		t.Fatalf("missing results %q", out)
	}
}
func TestRegistryClearsPasswordOnlyWhenExplicit(t *testing.T) {
	endpoint(t, "UpdateUpstreamRegistry", &v1.UpdateUpstreamRegistryRequest{UpstreamRegistryId: "id", Password: proto.String("")}, &v1.UpdateUpstreamRegistryResponse{}, 200, "")
	if _, err := execute([]string{"upstream", "update", "id", "--password-stdin"}, ""); err != nil {
		t.Fatal(err)
	}
}
func TestRegistryAlwaysShowsPublicHelp(t *testing.T) {
	// Even a proxy container's environment must not select an internal mode.
	for _, key := range []string{"CA_CERT", "KEY", "CERT", "ADDR", "SERVER_NAME", "MANIFEST", "CONFIG"} {
		t.Setenv(key, "invalid-base64!")
	}
	for _, args := range [][]string{nil, {"--help"}} {
		out, err := execute(args, "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "repository") {
			t.Fatalf("missing public help: %q", out)
		}
	}
}

func TestRegistryPasswordLimitAndTokenFlag(t *testing.T) {
	endpoint(t, "ListRepositories", &v1.ListRepositoriesRequest{Page: 1, PageSize: 25}, &v1.ListRepositoriesResponse{}, 200, "")
	t.Setenv("DEPOT_TOKEN", "wrong-env-token")
	if _, err := execute([]string{"repository", "list", "--token", "org-test"}, ""); err != nil {
		t.Fatal(err)
	}
	out, err := execute([]string{"upstream", "update", "id", "--password-stdin"}, strings.Repeat("x", 65537))
	if err == nil || err.Error() != "password exceeds 64 KiB" || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
