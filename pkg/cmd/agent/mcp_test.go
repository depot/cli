package agent

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
)

type fakeMcpService struct {
	agentv1connect.UnimplementedDepotAgentServiceHandler

	mu         sync.Mutex
	servers    []*agentv1.DepotAgentMcpServer
	creates    []*agentv1.CreateMcpServerRequest
	tokens     []*agentv1.SetMcpServerTokenRequest
	deletes    []string
	createErr  error
	loginURL   string
	loginCalls int
}

func (f *fakeMcpService) CreateMcpServer(_ context.Context, req *connect.Request[agentv1.CreateMcpServerRequest]) (*connect.Response[agentv1.CreateMcpServerResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, req.Msg)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return connect.NewResponse(&agentv1.CreateMcpServerResponse{McpServer: &agentv1.DepotAgentMcpServer{McpServerId: "mcp1", Name: req.Msg.Name, Scope: req.Msg.GetScope(), Auth: "none"}}), nil
}

func (f *fakeMcpService) ListMcpServers(context.Context, *connect.Request[agentv1.ListMcpServersRequest]) (*connect.Response[agentv1.ListMcpServersResponse], error) {
	return connect.NewResponse(&agentv1.ListMcpServersResponse{McpServers: f.servers}), nil
}

func (f *fakeMcpService) DeleteMcpServer(_ context.Context, req *connect.Request[agentv1.DeleteMcpServerRequest]) (*connect.Response[agentv1.DeleteMcpServerResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, req.Msg.McpServerId)
	return connect.NewResponse(&agentv1.DeleteMcpServerResponse{}), nil
}

func (f *fakeMcpService) SetMcpServerToken(_ context.Context, req *connect.Request[agentv1.SetMcpServerTokenRequest]) (*connect.Response[agentv1.SetMcpServerTokenResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, req.Msg)
	return connect.NewResponse(&agentv1.SetMcpServerTokenResponse{McpServer: &agentv1.DepotAgentMcpServer{Auth: "none"}}), nil
}

func (f *fakeMcpService) StartMcpLogin(context.Context, *connect.Request[agentv1.StartMcpLoginRequest]) (*connect.Response[agentv1.StartMcpLoginResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginCalls++
	return connect.NewResponse(&agentv1.StartMcpLoginResponse{AuthorizationUrl: f.loginURL}), nil
}

// runMcp runs `depot agent mcp <args>` against f, with stdin as the command's input.
func runMcp(t *testing.T, f *fakeMcpService, stdin string, args ...string) error {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewDepotAgentServiceHandler(f))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	prev := newClient
	newClient = func() agentv1connect.DepotAgentServiceClient {
		return agentv1connect.NewDepotAgentServiceClient(srv.Client(), srv.URL)
	}
	t.Cleanup(func() { newClient = prev })

	root := NewCmdAgent()
	root.SetArgs(append(append([]string{"mcp"}, args...), "--token", "tok", "--org", "org"))
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SilenceErrors, root.SilenceUsage = true, true
	return root.Execute()
}

var twoLinears = []*agentv1.DepotAgentMcpServer{
	{McpServerId: "org1", Name: "linear", Scope: "organization", Auth: "oauth"},
	{McpServerId: "usr1", Name: "linear", Scope: "user", Auth: "none"},
	{McpServerId: "usr2", Name: "sentry", Scope: "user", Auth: "token"},
}

func TestMcpAddSendsScopeAndHeaders(t *testing.T) {
	f := &fakeMcpService{}
	if err := runMcp(t, f, "", "add", "linear", "https://mcp.example.com/mcp", "--scope", "user"); err != nil {
		t.Fatal(err)
	}
	if err := runMcp(t, f, "", "add", "sentry", "https://mcp.example.com/mcp", "-H", "Authorization: Bearer ${secrets.T}"); err != nil {
		t.Fatal(err)
	}
	if got := f.creates[0].GetScope(); got != "user" {
		t.Fatalf("scope = %q, want user", got)
	}
	if f.creates[1].Scope != nil {
		t.Fatalf("scope should be left to the server's default, got %q", f.creates[1].GetScope())
	}
	if got := f.creates[1].Headers["Authorization"]; got != "Bearer ${secrets.T}" {
		t.Fatalf("header = %q", got)
	}
}

func TestMcpAddRejectsBadInputBeforeCallingDepot(t *testing.T) {
	f := &fakeMcpService{}
	for _, args := range [][]string{
		{"add", "x", "https://a", "--scope", "team"},
		{"add", "x", "https://a", "-H", "no-colon"},
		{"add", "x", "https://a", "--oauth-client-secret", "${secrets.S}"},
	} {
		if err := runMcp(t, f, "", args...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	if len(f.creates) != 0 {
		t.Fatalf("sent %d creates, want none", len(f.creates))
	}
}

func TestMcpAddSuggestsUserScopeWhenNotAnOwner(t *testing.T) {
	f := &fakeMcpService{createErr: connect.NewError(connect.CodePermissionDenied, errors.New("owner role required"))}
	err := runMcp(t, f, "", "add", "linear", "https://mcp.example.com/mcp")
	if err == nil || !strings.Contains(err.Error(), "--scope user") {
		t.Fatalf("got %v, want a hint to pass --scope user", err)
	}
}

func TestMcpTokenReadsStdinAndResolvesTheName(t *testing.T) {
	f := &fakeMcpService{servers: twoLinears}
	if err := runMcp(t, f, "s3cret\n", "token", "sentry"); err != nil {
		t.Fatal(err)
	}
	if len(f.tokens) != 1 || f.tokens[0].McpServerId != "usr2" || f.tokens[0].Token != "s3cret" {
		t.Fatalf("got %v", f.tokens)
	}
}

func TestMcpTokenClearSendsAnEmptyTokenAndRefusesAnEmptyOne(t *testing.T) {
	f := &fakeMcpService{servers: twoLinears}
	if err := runMcp(t, f, "", "token", "sentry"); err == nil {
		t.Fatal("an empty token should need --clear")
	}
	if err := runMcp(t, f, "ignored", "token", "sentry", "--clear"); err != nil {
		t.Fatal(err)
	}
	if len(f.tokens) != 1 || f.tokens[0].Token != "" {
		t.Fatalf("got %v", f.tokens)
	}
}

func TestMcpRemoveNeedsScopeWhenANameIsShared(t *testing.T) {
	f := &fakeMcpService{servers: twoLinears}
	if err := runMcp(t, f, "", "remove", "linear"); err == nil || !strings.Contains(err.Error(), "--scope") {
		t.Fatalf("got %v, want a hint to pass --scope", err)
	}
	if err := runMcp(t, f, "", "remove", "linear", "--scope", "user"); err != nil {
		t.Fatal(err)
	}
	if err := runMcp(t, f, "", "rm", "org1"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.deletes, ","); got != "usr1,org1" {
		t.Fatalf("deleted %s", got)
	}
}

func TestMcpLoginRefusesANonHTTPSURL(t *testing.T) {
	f := &fakeMcpService{servers: twoLinears, loginURL: "file:///etc/passwd"}
	if err := runMcp(t, f, "", "login", "sentry", "--no-browser"); err == nil {
		t.Fatal("want an error for a non-https login URL")
	}
	f.loginURL = "https://auth.example.com/authorize?state=x"
	if err := runMcp(t, f, "", "login", "sentry", "--no-browser"); err != nil {
		t.Fatal(err)
	}
	if f.loginCalls != 2 {
		t.Fatalf("login calls = %d", f.loginCalls)
	}
}

func TestMcpTableShowsScopeAndAuth(t *testing.T) {
	var buf bytes.Buffer
	servers := append(twoLinears, &agentv1.DepotAgentMcpServer{Name: "evil\u001b[2J", Scope: "user", Auth: "none"})
	if err := writeMcpTable(&buf, servers); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"SCOPE", "AUTH", "organization  oauth", "user          token"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\u001b") {
		t.Fatalf("table passes an escape through:\n%q", out)
	}
}

func TestReadTokenBoundsPipedInput(t *testing.T) {
	if _, err := readToken(strings.NewReader(strings.Repeat("a", maxTokenBytes+1)), &bytes.Buffer{}); err == nil {
		t.Fatal("want an error for an oversized token")
	}
	got, err := readToken(strings.NewReader("  tok\r\n"), &bytes.Buffer{})
	if err != nil || got != "tok" {
		t.Fatalf("got %q, %v", got, err)
	}
}
