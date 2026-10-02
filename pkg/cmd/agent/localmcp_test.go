package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain lets the test binary double as a stdio MCP server, so the tests start a real one.
func TestMain(m *testing.M) {
	if os.Getenv("DEPOT_TEST_MCP_SERVER") == "1" {
		runNotesServer()
		return
	}
	os.Exit(m.Run())
}

type readFileArgs struct {
	Path string `json:"path"`
}

func runNotesServer() {
	server := mcp.NewServer(&mcp.Implementation{Name: "notes", Description: "Notes on this laptop."}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "read_file", Description: "Reads a file."},
		func(_ context.Context, _ *mcp.CallToolRequest, args readFileArgs) (*mcp.CallToolResult, any, error) {
			if args.Path == "crash" {
				os.Exit(3)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: os.Getenv("NOTES_PREFIX") + args.Path}}}, nil, nil
		})
	_ = server.Run(context.Background(), &mcp.StdioTransport{})
}

func writeMcpConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".mcp.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func notesConfig(t *testing.T) string {
	t.Setenv("DEPOT_TEST_BINARY", os.Args[0])
	t.Setenv("DEPOT_TEST_PREFIX", "contents of ")
	return writeMcpConfig(t, `{"mcpServers": {
		"notes": {"command": "${DEPOT_TEST_BINARY}", "env": {"DEPOT_TEST_MCP_SERVER": "1", "NOTES_PREFIX": "${DEPOT_TEST_PREFIX}"}},
		"docs": {"type": "http", "url": "https://mcp.example.com"}
	}}`)
}

func TestLoadMcpConfigKeepsStdioServersAndExpandsTheEnvironment(t *testing.T) {
	servers, remote, err := loadMcpConfig(notesConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].name != "notes" || servers[0].Command != os.Args[0] || servers[0].Env["NOTES_PREFIX"] != "contents of " {
		t.Fatalf("servers = %+v", servers)
	}
	if len(remote) != 1 || remote[0] != "docs" {
		t.Fatalf("remote = %v, want [docs]", remote)
	}

	_, _, err = loadMcpConfig(writeMcpConfig(t, `{"mcpServers": {"My_Notes": {"command": "notes"}}}`))
	if err == nil || !strings.Contains(err.Error(), `"My_Notes"`) {
		t.Fatalf("expected the bad name to be refused, got %v", err)
	}
}

func TestStartServerSkipsToolsPastTheOfferBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	servers, _, err := loadMcpConfig(notesConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	var notices syncBuffer
	budget := 100
	cs, offer, err := startServer(ctx, client, servers[0], &budget, &notices)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if len(offer.Tools) != 0 || !strings.Contains(notices.String(), "skipping tool read_file") {
		t.Fatalf("tools = %v, notices = %q", offer.Tools, notices.String())
	}
}

// fakeLocalMcpService plays Depot's side: scripts[i] runs the i-th PullLocalMcpCalls stream.
type fakeLocalMcpService struct {
	agentv1connect.UnimplementedDepotAgentServiceHandler

	mu      sync.Mutex
	pulls   []*agentv1.PullLocalMcpCallsRequest
	scripts []func(*connect.ServerStream[agentv1.PullLocalMcpCallsResponse]) error
	answers chan *agentv1.RespondLocalMcpCallRequest
}

func (f *fakeLocalMcpService) PullLocalMcpCalls(_ context.Context, req *connect.Request[agentv1.PullLocalMcpCallsRequest], stream *connect.ServerStream[agentv1.PullLocalMcpCallsResponse]) error {
	f.mu.Lock()
	f.pulls = append(f.pulls, req.Msg)
	script := f.scripts[len(f.pulls)-1]
	f.mu.Unlock()
	return script(stream)
}

func (f *fakeLocalMcpService) RespondLocalMcpCall(_ context.Context, req *connect.Request[agentv1.RespondLocalMcpCallRequest]) (*connect.Response[agentv1.RespondLocalMcpCallResponse], error) {
	f.answers <- req.Msg
	return connect.NewResponse(&agentv1.RespondLocalMcpCallResponse{}), nil
}

func attachedAs(id string) *agentv1.PullLocalMcpCallsResponse {
	return &agentv1.PullLocalMcpCallsResponse{Call: &agentv1.PullLocalMcpCallsResponse_Attached{Attached: &agentv1.LocalMcpAttached{AttachmentId: id}}}
}

func callTool(id, server, args string) *agentv1.PullLocalMcpCallsResponse {
	return &agentv1.PullLocalMcpCallsResponse{Call: &agentv1.PullLocalMcpCallsResponse_CallTool{CallTool: &agentv1.LocalMcpToolCall{
		CallId: id, ServerName: server, ToolName: "read_file", ArgumentsJson: args,
	}}}
}

func TestServeRunsTheAgentsCallsOnARealServerUntilReplaced(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	servers, _, err := loadMcpConfig(notesConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	var notices syncBuffer
	local := startLocalMcp(ctx, servers, &notices)
	defer local.Close()
	if len(local.offers) != 1 || local.offers[0].Description != "Notes on this laptop." ||
		len(local.offers[0].Tools) != 1 || !strings.Contains(local.offers[0].Tools[0].InputSchemaJson, `"path"`) {
		t.Fatalf("offers = %v, notices = %q", local.offers, notices.String())
	}

	f := &fakeLocalMcpService{answers: make(chan *agentv1.RespondLocalMcpCallRequest)}
	var answers []*agentv1.RespondLocalMcpCallRequest
	ask := func(stream *connect.ServerStream[agentv1.PullLocalMcpCallsResponse], msg *agentv1.PullLocalMcpCallsResponse) error {
		if err := stream.Send(msg); err != nil {
			return err
		}
		answers = append(answers, <-f.answers)
		return nil
	}
	f.scripts = []func(*connect.ServerStream[agentv1.PullLocalMcpCallsResponse]) error{
		// The first stream runs its course; the CLI attaches again.
		func(stream *connect.ServerStream[agentv1.PullLocalMcpCallsResponse]) error {
			return stream.Send(attachedAs("dala_1"))
		},
		func(stream *connect.ServerStream[agentv1.PullLocalMcpCallsResponse]) error {
			for _, msg := range []*agentv1.PullLocalMcpCallsResponse{
				attachedAs("dala_2"),
				callTool("dalc_1", "notes", `{"path":"todo.txt"}`),
				callTool("dalc_2", "ghost", `{}`),
				callTool("dalc_3", "notes", `{"path":"crash"}`),
				callTool("dalc_4", "notes", `{"path":"todo.txt"}`),
			} {
				if msg.GetAttached() != nil {
					if err := stream.Send(msg); err != nil {
						return err
					}
					continue
				}
				if err := ask(stream, msg); err != nil {
					return err
				}
			}
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("A newer attach replaced these local MCP servers"))
		},
	}
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewDepotAgentServiceHandler(f))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := &session{client: agentv1connect.NewDepotAgentServiceClient(srv.Client(), srv.URL), token: "tok", orgID: "org"}

	local.serve(ctx, s, "s1", &notices)
	if ctx.Err() != nil {
		t.Fatal("serve returned only because the test timed out")
	}
	if len(f.pulls) != 2 || f.pulls[1].GetSessionId() != "s1" || len(f.pulls[1].GetServers()) != 1 {
		t.Fatalf("pulls = %v", f.pulls)
	}
	if len(answers) != 4 {
		t.Fatalf("answers = %v", answers)
	}
	if a := answers[0]; a.GetAttachmentId() != "dala_2" || a.GetCallId() != "dalc_1" || a.GetResult().GetIsError() ||
		!strings.Contains(a.GetResult().GetContentJson(), "contents of todo.txt") {
		t.Errorf("a call to the server = %v", a)
	}
	if a := answers[1]; !a.GetResult().GetIsError() || !strings.Contains(a.GetResult().GetContentJson(), `named \"ghost\"`) {
		t.Errorf("a call to a server never offered = %v", a)
	}
	// The server exits mid-call, and then is gone: both are error results, not a crash.
	for _, a := range answers[2:] {
		if !a.GetResult().GetIsError() || !strings.Contains(a.GetResult().GetContentJson(), "local MCP server notes failed") {
			t.Errorf("a call to a server that is gone = %v", a)
		}
	}
	out := notices.String()
	if strings.Count(out, "offering 1 local MCP servers") != 1 || !strings.Contains(out, "yours are no longer offered") {
		t.Errorf("notices = %q", out)
	}
}
