package org

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/depot/cli/pkg/proto/depot/core/v1"
	"github.com/depot/cli/pkg/proto/depot/core/v1/corev1connect"
	"github.com/spf13/viper"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestOrganizationTokenLifecycle(t *testing.T) {
	service := setupOrganizationTokenService(t)

	stdout, stderr, err := executeTokenCommand("create", "--description", "  CI builds  ", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		Token  organizationTokenOutput `json:"token"`
		Secret string                  `json:"secret"`
	}
	if err := json.Unmarshal([]byte(stdout), &created); err != nil {
		t.Fatalf("creation output is not JSON: %v\n%s", err, stdout)
	}
	if created.Token.ID != "token-1" || created.Token.Description != "CI builds" || created.Secret != service.secret {
		t.Fatalf("unexpected created token: %#v", created)
	}
	if strings.Count(stdout, service.secret) != 1 || strings.Contains(stderr, service.secret) || !strings.Contains(stderr, "cannot be retrieved again") {
		t.Fatalf("secret must appear once on stdout with separate storage guidance: stdout=%q stderr=%q", stdout, stderr)
	}
	if created.Token.CreatedAt != "2026-10-16T12:00:00Z" {
		t.Fatalf("created_at = %q", created.Token.CreatedAt)
	}
	if got := viper.GetString("api_token"); got != "owner-user-token" {
		t.Fatalf("creation replaced the saved user credential: %q", got)
	}

	stdout, stderr, err = executeTokenCommand("list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Tokens  []organizationTokenOutput `json:"tokens"`
		HasMore bool                      `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Tokens) != 1 || listed.Tokens[0] != created.Token || listed.HasMore {
		t.Fatalf("unexpected listed metadata: %#v", listed)
	}
	if strings.Contains(stdout+stderr, service.secret) || strings.Contains(stdout, "secret") || strings.Contains(stdout, "hash") {
		t.Fatalf("list leaked sensitive fields: %q %q", stdout, stderr)
	}
	if service.page != 1 || service.pageSize != 100 {
		t.Fatalf("default pagination = %d/%d", service.page, service.pageSize)
	}

	for range 2 {
		stdout, stderr, err = executeTokenCommand("revoke", created.Token.ID, "--output", "json")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(stdout) != "{}" || stderr != "" {
			t.Fatalf("unexpected revoke output: %q %q", stdout, stderr)
		}
	}
	stdout, _, err = executeTokenCommand("list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil || len(listed.Tokens) != 0 || strings.Contains(stdout, "null") {
		t.Fatalf("revoked token still listed or empty list is not an array: %q, %v", stdout, err)
	}
}

func TestOrganizationTokenHumanOutputAndPagination(t *testing.T) {
	service := setupOrganizationTokenService(t)
	stdout, stderr, err := executeTokenCommand("create", "--description", "CI\nbuilds\x1b[31m")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(stdout, service.secret) != 1 || strings.Contains(stderr, service.secret) || strings.Contains(stdout, "\x1b") || !strings.Contains(stdout, `CI\nbuilds\x1b[31m`) {
		t.Fatalf("unexpected human creation output: %q %q", stdout, stderr)
	}
	service.hasMore = true
	stdout, stderr, err = executeTokenCommand("list", "--page", "2", "--page-size", "1")
	if err != nil {
		t.Fatal(err)
	}
	if service.page != 2 || service.pageSize != 1 || !strings.Contains(stderr, "--page 3") || !strings.Contains(stdout, "Never") {
		t.Fatalf("pagination/output = %d/%d %q %q", service.page, service.pageSize, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, service.secret) || strings.Contains(stdout, "\x1b") {
		t.Fatalf("unsafe list output: %q %q", stdout, stderr)
	}
	stdout, stderr, err = executeTokenCommand("revoke", "token-1")
	if err != nil || stdout != "Token revoked.\n" || stderr != "" {
		t.Fatalf("unexpected revocation output: %q %q %v", stdout, stderr, err)
	}
	if service.revokedID != "token-1" {
		t.Fatalf("revoked ID = %q", service.revokedID)
	}
	stdout, stderr, err = executeTokenCommand("revoke", "unrelated-token")
	if connect.CodeOf(err) != connect.CodeNotFound || stdout != "" || stderr != "" {
		t.Fatalf("unknown token should return not found without success output: %q %q %v", stdout, stderr, err)
	}
}

func TestOrganizationTokenAuthenticationAndOrganizationSelection(t *testing.T) {
	for _, source := range []string{"saved", "environment", "flags"} {
		t.Run(source, func(t *testing.T) {
			service := setupOrganizationTokenService(t)
			args := []string{"list", "--output", "json"}
			switch source {
			case "environment":
				viper.Reset()
				viper.SetEnvPrefix("DEPOT")
				viper.AutomaticEnv()
				t.Setenv("DEPOT_ORG_ID", "org-123")
				t.Setenv("DEPOT_TOKEN", "owner-user-token")
			case "flags":
				viper.Set("org_id", "other-org")
				viper.Set("api_token", "other-saved-token")
				t.Setenv("DEPOT_TOKEN", "other-env-token")
				args = append(args, "--organization", "org-123", "--token", "owner-user-token")
			}
			stdout, stderr, err := executeTokenCommand(args...)
			if err != nil || service.calls != 1 || !json.Valid([]byte(stdout)) || stderr != "" {
				t.Fatalf("credential selection failed: calls=%d stdout=%q stderr=%q err=%v", service.calls, stdout, stderr, err)
			}
		})
	}
}

func TestOrganizationTokenInputsFailBeforeNetwork(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		prepare func()
		message string
	}{
		{name: "empty description", args: []string{"create", "--description", "  "}, message: "--description"},
		{name: "long description", args: []string{"create", "--description", strings.Repeat("🦊", 192)}, message: "--description"},
		{name: "invalid page", args: []string{"list", "--page", "0"}, message: "--page"},
		{name: "invalid size", args: []string{"list", "--page-size", "101"}, message: "--page-size"},
		{name: "invalid format", args: []string{"create", "--description", "CI", "--output", "xml"}, message: "unsupported output"},
		{name: "empty ID", args: []string{"revoke", " "}, message: "token ID"},
		{name: "missing ID", args: []string{"revoke"}, message: "accepts 1 arg"},
		{name: "missing organization", args: []string{"list"}, prepare: func() { viper.Set("org_id", "") }, message: "missing organization"},
		{name: "missing user credential", args: []string{"create", "--description", "CI"}, prepare: func() { viper.Set("api_token", "") }, message: "depot login"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := setupOrganizationTokenService(t)
			// JIT/cache credentials must not silently replace a missing user token.
			t.Setenv("DEPOT_JIT_TOKEN", "jit-token")
			t.Setenv("DEPOT_CACHE_TOKEN", "cache-token")
			if test.prepare != nil {
				test.prepare()
			}
			stdout, stderr, err := executeTokenCommand(test.args...)
			if err == nil || !strings.Contains(err.Error(), test.message) || service.calls != 0 || stdout != "" || stderr != "" {
				t.Fatalf("expected validation before network: calls=%d stdout=%q stderr=%q err=%v", service.calls, stdout, stderr, err)
			}
		})
	}
}

func TestOrganizationTokenDescriptionAccepts191UnicodeCharacters(t *testing.T) {
	service := setupOrganizationTokenService(t)
	description := strings.Repeat("🦊", 191)
	_, _, err := executeTokenCommand("create", "--description", description, "--output", "json")
	if err != nil || service.token == nil || service.token.GetDescription() != description {
		t.Fatalf("Unicode description rejected or corrupted: %v", err)
	}
}

func TestOrganizationTokenErrorsDoNotRetryOrPrintSuccess(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeUnimplemented, connect.CodeResourceExhausted, connect.CodeUnavailable} {
		for _, args := range [][]string{{"create", "--description", "CI"}, {"list"}, {"revoke", "token-1"}} {
			t.Run(code.String()+"/"+args[0], func(t *testing.T) {
				service := setupOrganizationTokenService(t)
				service.failure = connect.NewError(code, errors.New("request rejected"))
				stdout, stderr, err := executeTokenCommand(args...)
				if err == nil || connect.CodeOf(err) != code || service.calls != 1 || stdout != "" || stderr != "" {
					t.Fatalf("failure should return without retries/output: calls=%d stdout=%q stderr=%q err=%v", service.calls, stdout, stderr, err)
				}
				if (code == connect.CodeUnauthenticated || code == connect.CodePermissionDenied) && !strings.Contains(err.Error(), "owner of the selected organization") {
					t.Fatalf("missing owner guidance: %v", err)
				}
				if code == connect.CodeUnimplemented && !strings.Contains(err.Error(), "not available on this API server") {
					t.Fatalf("missing server compatibility guidance: %v", err)
				}
			})
		}
	}
}

func TestOrganizationTokenIncompleteCreationResponse(t *testing.T) {
	service := setupOrganizationTokenService(t)
	service.secret = ""
	stdout, stderr, err := executeTokenCommand("create", "--description", "CI", "--output", "json")
	if err == nil || !strings.Contains(err.Error(), "list tokens before retrying") || service.calls != 1 || stdout != "" || stderr != "" {
		t.Fatalf("incomplete response must fail without retry: calls=%d stdout=%q stderr=%q err=%v", service.calls, stdout, stderr, err)
	}
}

func executeTokenCommand(args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	command := NewCmdOrg()
	command.SetArgs(append([]string{"tokens"}, args...))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SilenceUsage = true
	command.SilenceErrors = true
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

type organizationTokenService struct {
	corev1connect.UnimplementedOrganizationTokenServiceHandler
	token     *corev1.OrganizationToken
	secret    string
	failure   error
	calls     int
	page      int32
	pageSize  int32
	hasMore   bool
	revokedID string
}

func setupOrganizationTokenService(t *testing.T) *organizationTokenService {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("org_id", "org-123")
	viper.Set("api_token", "owner-user-token")
	t.Setenv("DEPOT_TOKEN", "")
	t.Setenv("DEPOT_ORG_ID", "")
	t.Setenv("DEPOT_API_TOKEN", "")
	service := &organizationTokenService{secret: "test-once-only-secret"}
	_, handler := corev1connect.NewOrganizationTokenServiceHandler(service)
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	return service
}

func (service *organizationTokenService) authenticate(request connect.AnyRequest) error {
	service.calls++
	if request.Header().Get("Authorization") != "Bearer owner-user-token" {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("owner user credential required"))
	}
	if request.Header().Get("x-depot-org") != "org-123" {
		return connect.NewError(connect.CodePermissionDenied, errors.New("organization access denied"))
	}
	return service.failure
}

func (service *organizationTokenService) ListTokens(_ context.Context, request *connect.Request[corev1.ListOrganizationTokensRequest]) (*connect.Response[corev1.ListOrganizationTokensResponse], error) {
	if err := service.authenticate(request); err != nil {
		return nil, err
	}
	service.page = request.Msg.GetPage()
	service.pageSize = request.Msg.GetPageSize()
	response := &corev1.ListOrganizationTokensResponse{HasMore: service.hasMore}
	if service.token != nil {
		response.Tokens = []*corev1.OrganizationToken{service.token}
	}
	return connect.NewResponse(response), nil
}

func (service *organizationTokenService) CreateToken(_ context.Context, request *connect.Request[corev1.CreateOrganizationTokenRequest]) (*connect.Response[corev1.CreateOrganizationTokenResponse], error) {
	if err := service.authenticate(request); err != nil {
		return nil, err
	}
	service.token = &corev1.OrganizationToken{
		Id:          "token-1",
		Description: request.Msg.GetDescription(),
		CreatedAt:   timestamppb.New(time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)),
	}
	return connect.NewResponse(&corev1.CreateOrganizationTokenResponse{Token: service.token, Secret: service.secret}), nil
}

func (service *organizationTokenService) RevokeToken(_ context.Context, request *connect.Request[corev1.RevokeOrganizationTokenRequest]) (*connect.Response[corev1.RevokeOrganizationTokenResponse], error) {
	if err := service.authenticate(request); err != nil {
		return nil, err
	}
	if service.token.GetId() == request.Msg.GetId() {
		service.token = nil
	} else if service.revokedID != request.Msg.GetId() {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("Not found"))
	}
	service.revokedID = request.Msg.GetId()
	return connect.NewResponse(&corev1.RevokeOrganizationTokenResponse{}), nil
}
