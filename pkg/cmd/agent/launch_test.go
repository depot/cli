package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func serveLaunch(t *testing.T, handle func(*connect.Request[agentv1.LaunchRequest]) (*agentv1.LaunchResponse, error)) {
	t.Helper()
	mux := http.NewServeMux()
	path := agentv1connect.DepotAgentLaunchServiceLaunchProcedure
	mux.Handle(path, connect.NewUnaryHandler(path, func(_ context.Context, req *connect.Request[agentv1.LaunchRequest]) (*connect.Response[agentv1.LaunchResponse], error) {
		resp, err := handle(req)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(resp), nil
	}))
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	for _, name := range []string{"DEPOT_TOKEN", "DEPOT_ORG_ID", "GITHUB_RUN_ID", "GITHUB_REPOSITORY", "GITHUB_JOB"} {
		t.Setenv(name, "")
	}
}

func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewCmdAgent()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"launch"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func launched(id string) *agentv1.LaunchResponse {
	return &agentv1.LaunchResponse{Session: &agentv1.DepotAgentSession{SessionId: id}}
}

func TestLaunchSendsTheRequestAndPrintsTheSessionURL(t *testing.T) {
	var got *connect.Request[agentv1.LaunchRequest]
	serveLaunch(t, func(req *connect.Request[agentv1.LaunchRequest]) (*agentv1.LaunchResponse, error) {
		got = req
		return launched("session-1"), nil
	})
	out, err := execute(t, "public-agent-code-review@v1", "--source", "depot-code:acme/app@feature/x", "--source", "github:acme/lib",
		"--message", "Review this", "--client-request-id", "key-1", "--org", "org-1", "--token", "user-token")
	if err != nil {
		t.Fatal(err)
	}
	if out != "https://depot.dev/orgs/org-1/agent-runs/sessions/session-1\n" {
		t.Fatalf("output = %q", out)
	}
	if got.Header().Get("Authorization") != "Bearer user-token" || got.Header().Get("x-depot-org") != "org-1" {
		t.Fatal("missing authentication or organization header")
	}
	msg := got.Msg
	if msg.Agent != "public-agent-code-review@v1" || msg.Message != "Review this" || msg.GetClientRequestId() != "key-1" || len(msg.Sources) != 2 {
		t.Fatalf("request = %v", msg)
	}
	if s := msg.Sources[0]; s.Kind != "depot-code" || s.Repository != "acme/app" || s.GetRef() != "feature/x" {
		t.Fatalf("first source = %v", s)
	}
	if s := msg.Sources[1]; s.Kind != "github" || s.Repository != "acme/lib" || s.Ref != nil {
		t.Fatalf("second source = %v", s)
	}
}

func TestLaunchInCIDerivesAStableKeyPerLaunch(t *testing.T) {
	var keys []string
	serveLaunch(t, func(req *connect.Request[agentv1.LaunchRequest]) (*agentv1.LaunchResponse, error) {
		keys = append(keys, req.Msg.GetClientRequestId())
		return launched("session-1"), nil
	})
	t.Setenv("GITHUB_RUN_ID", "123")
	t.Setenv("GITHUB_REPOSITORY", "acme/app")
	t.Setenv("GITHUB_JOB", "review")
	for _, message := range []string{"Review this", "Review this", "Summarize this"} {
		if _, err := execute(t, "public-agent-code-review", "--source", "depot-code:acme/app", "--message", message, "--token", "t"); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasPrefix(keys[0], "ci:") || len(keys[0]) > 191 || keys[0] != keys[1] || keys[0] == keys[2] {
		t.Fatalf("keys = %v", keys)
	}
}

func TestLaunchExplainsTheCIJobTokenRefusal(t *testing.T) {
	for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeUnauthenticated} {
		serveLaunch(t, func(*connect.Request[agentv1.LaunchRequest]) (*agentv1.LaunchResponse, error) {
			return nil, connect.NewError(code, errors.New("refused"))
		})
		t.Setenv("DEPOT_TOKEN", "depot_cijob_abc")
		_, err := execute(t, "public-agent-code-review", "--source", "depot-code:acme/app", "--message", "Review this")
		if err == nil || !strings.Contains(err.Error(), "does not accept the CI job token") {
			t.Fatalf("%v: error = %v", code, err)
		}
	}
}

func TestLaunchInCIUsesTheJobTokenAndPrefersTheReturnedURL(t *testing.T) {
	var got *connect.Request[agentv1.LaunchRequest]
	serveLaunch(t, func(req *connect.Request[agentv1.LaunchRequest]) (*agentv1.LaunchResponse, error) {
		got = req
		resp := launched("session-1")
		resp.Url = "https://depot.dev/orgs/org-1/agent-runs/sessions/session-1"
		return resp, nil
	})
	t.Setenv("DEPOT_TOKEN", "depot_cijob_abc")
	t.Setenv("DEPOT_ORG_ID", "org-from-ci")
	out, err := execute(t, "public-agent-code-review", "--source", "github:acme/app", "--message", "Review this")
	if err != nil {
		t.Fatal(err)
	}
	if got.Header().Get("Authorization") != "Bearer depot_cijob_abc" || got.Header().Get("x-depot-org") != "org-from-ci" {
		t.Fatal("the job token and its org were not sent")
	}
	if out != "https://depot.dev/orgs/org-1/agent-runs/sessions/session-1\n" {
		t.Fatalf("output = %q", out)
	}
}

func TestLaunchWithoutAnOrgPrintsTheSessionID(t *testing.T) {
	serveLaunch(t, func(*connect.Request[agentv1.LaunchRequest]) (*agentv1.LaunchResponse, error) {
		return launched("session-1"), nil
	})
	t.Setenv("HOME", t.TempDir())
	out, err := execute(t, "public-agent-code-review", "--source", "depot-code:acme/app", "--message", "Review this", "--token", "t")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "session-1") {
		t.Fatalf("output = %q", out)
	}
}

func TestParseSourceRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"acme/app", "depot-code:acme", "depot-code:/app", ":acme/app", "depot-code:acme/app@"} {
		if _, err := parseSource(value); err == nil {
			t.Errorf("parseSource(%q) succeeded", value)
		}
	}
}
