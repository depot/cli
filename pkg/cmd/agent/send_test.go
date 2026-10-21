package agent

import (
	"bytes"
	"context"
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
	"google.golang.org/protobuf/proto"
)

func serveSend(t *testing.T, handle func(*connect.Request[agentv1.SendInputRequest]) (*agentv1.SendInputResponse, error)) {
	t.Helper()
	mux := http.NewServeMux()
	path := agentv1connect.DepotAgentServiceSendInputProcedure
	mux.Handle(path, connect.NewUnaryHandler(path, func(_ context.Context, req *connect.Request[agentv1.SendInputRequest]) (*connect.Response[agentv1.SendInputResponse], error) {
		resp, err := handle(req)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(resp), nil
	}))
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	t.Setenv("DEPOT_TOKEN", "")
	t.Setenv("DEPOT_ORG_ID", "")
}

func executeSend(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewCmdAgent()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"send"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func sent(id string) *agentv1.SendInputResponse {
	return &agentv1.SendInputResponse{Input: &agentv1.DepotAgentInput{InputId: id}}
}

func TestSendTargetsTheSessionByID(t *testing.T) {
	var got *connect.Request[agentv1.SendInputRequest]
	serveSend(t, func(req *connect.Request[agentv1.SendInputRequest]) (*agentv1.SendInputResponse, error) {
		got = req
		return sent("dai_1"), nil
	})

	out, err := executeSend(t, "das_1", "--message", "hello", "--mode", "steer", "--client-request-id", "push:1", "--token", "tok", "--org", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	msg := got.Msg
	if msg.GetSessionId() != "das_1" || msg.GetContent() != "hello" || msg.GetMode() != "steer" || msg.GetClientRequestId() != "push:1" || len(msg.GetContributions()) != 0 {
		t.Fatalf("unexpected request %v", msg)
	}
	if got.Header().Get("Authorization") != "Bearer tok" || got.Header().Get("x-depot-org") != "org-1" {
		t.Fatalf("unexpected headers %v", got.Header())
	}
	if want := "Sent dai_1 to session das_1\nhttps://depot.dev/orgs/org-1/agent-runs/sessions/das_1\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestSendTargetsTheSessionByKey(t *testing.T) {
	var got *agentv1.SendInputRequest
	serveSend(t, func(req *connect.Request[agentv1.SendInputRequest]) (*agentv1.SendInputResponse, error) {
		got = req.Msg
		return sent("dai_1"), nil
	})

	if _, err := executeSend(t, "--key", " catchup:app:merge:abc ", "--message", "hello", "--token", "tok", "--org", "org1"); err != nil {
		t.Fatal(err)
	}
	if got.GetSessionId() != "das_c1e46ac22d4368d28e687082621bb966" || got.GetMode() != "followup" || got.ClientRequestId != nil {
		t.Fatalf("unexpected request %v", got)
	}
}

// Expected IDs come from the API's own derivation, run in Node.
func TestSessionIDForKeyMatchesTheAPI(t *testing.T) {
	id, err := sessionIDForKey("org1", "a<b>& \"\\\n\t\x01é")
	if err != nil {
		t.Fatal(err)
	}
	if id != "das_dd6ddd5b2bab609c28d129d3b104f471" {
		t.Fatalf("id = %s", id)
	}
	for key, want := range map[string]string{"\ufeffkey": "das_047f96037011703fe919fbc7d163b81e", "\u0085key": "das_613f778bad4f6124edc1fd8ecccbc3fa"} {
		if id, _ := sessionIDForKey("org1", key); id != want {
			t.Fatalf("id for %q = %s, want %s", key, id, want)
		}
	}
}

func TestSendToASubagent(t *testing.T) {
	var got *agentv1.SendInputRequest
	serveSend(t, func(req *connect.Request[agentv1.SendInputRequest]) (*agentv1.SendInputResponse, error) {
		got = req.Msg
		return sent("dai_1"), nil
	})

	if _, err := executeSend(t, "das_1", "--message", "hello", "--subagent", "2", "--mode", "steer", "--token", "tok"); err != nil {
		t.Fatal(err)
	}
	var target agentv1.DepotAgentSubagentsInput
	if err := proto.Unmarshal(got.GetContributions()["subagents"], &target); err != nil {
		t.Fatal(err)
	}
	if got.GetMode() != "subagent" || target.GetConversationId() != "2" || target.GetFollowUp() {
		t.Fatalf("unexpected request %v, target %v", got, &target)
	}
}

func TestSendRejectsBadArguments(t *testing.T) {
	serveSend(t, func(*connect.Request[agentv1.SendInputRequest]) (*agentv1.SendInputResponse, error) {
		t.Fatal("no request expected")
		return nil, nil
	})
	for _, args := range [][]string{
		{"--message", "hi", "--token", "tok"},
		{"das_1", "--key", "k", "--message", "hi", "--token", "tok", "--org", "o"},
		{"das_1", "--message", "hi", "--mode", "interrupt", "--token", "tok"},
		{"das_1", "--message", " ", "--token", "tok"},
	} {
		if _, err := executeSend(t, args...); err == nil {
			t.Fatalf("expected an error for %s", strings.Join(args, " "))
		}
	}
}
