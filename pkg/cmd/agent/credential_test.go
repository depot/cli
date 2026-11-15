package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"github.com/spf13/viper"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// serveSandbox is the API as a Dispatch sandbox reaches it: a turn token URL and a cache token, no static token.
// It returns the credential each RPC was called with.
func serveSandbox(t *testing.T, f *fakeAgent, cacheToken string) func() map[string]string {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]string{}
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewDepotAgentServiceHandler(f))
	launch := agentv1connect.DepotAgentLaunchServiceLaunchProcedure
	mux.Handle(launch, connect.NewUnaryHandler(launch, func(context.Context, *connect.Request[agentv1.LaunchRequest]) (*connect.Response[agentv1.LaunchResponse], error) {
		return connect.NewResponse(launched("das_1")), nil
	}))
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[path.Base(r.URL.Path)] = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Unlock()
		mux.ServeHTTP(w, r)
	}), &http2.Server{}))
	t.Cleanup(server.Close)
	turn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"value":"turn-token"}`)
	}))
	t.Cleanup(turn.Close)
	saved := viper.GetString("api_token")
	viper.Set("api_token", "")
	t.Cleanup(func() { viper.Set("api_token", saved) })
	for name, value := range map[string]string{
		"DEPOT_API_URL": server.URL, "DEPOT_ORG_ID": "org-1", "DEPOT_API_TOKEN_URL": turn.URL, "DEPOT_CACHE_TOKEN": cacheToken,
		"DEPOT_TOKEN": "", "DEPOT_JIT_TOKEN": "", "DEPOT_RUN_TOKEN_URL": "", "GITHUB_RUN_ID": "", "GITHUB_REPOSITORY": "",
	} {
		t.Setenv(name, value)
	}
	return func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		got := seen
		seen = map[string]string{}
		return got
	}
}

// The API takes a turn's token on CreateSession, GetSession, GetSessionMessage and ListDepotAgentSessions only.
func TestEachRPCGetsTheCredentialTheAPITakes(t *testing.T) {
	long := `{"messages": [{"id": "3", "seq": 3, "role": "user", "inputId": "dai_1", "text": "hello"},
		{"id": "6", "seq": 6, "role": "assistant", "text": "cut\n[9 bytes truncated]", "truncatedBytes": 9}], "tools": []}`
	session := &agentv1.DepotAgentSession{SessionId: "das_1", Status: "idle"}
	replied := func() *fakeAgent {
		return &fakeAgent{
			got:    &agentv1.GetSessionResponse{Session: session, ViewJson: long, ViewSeq: 7},
			frames: [][]*agentv1.WatchSessionResponse{{frame("idle", "c1", long)}},
			whole:  map[string]*agentv1.GetSessionMessageResponse{"6": {Role: "assistant", Text: "whole"}},
		}
	}
	for _, tc := range []struct {
		name string
		args []string
		want map[string]string
	}{
		{"create", []string{"create", "hello"}, map[string]string{"CreateSession": "turn-token"}},
		{"create --watch", []string{"create", "hello", "--watch"}, map[string]string{"CreateSession": "cache-token", "WatchSession": "cache-token", "GetSessionMessage": "cache-token"}},
		{"list", []string{"list"}, map[string]string{"ListDepotAgentSessions": "turn-token"}},
		{"logs", []string{"logs", "das_1"}, map[string]string{"GetSession": "turn-token", "GetSessionMessage": "turn-token"}},
		{"watch", []string{"watch", "das_1", "--until-idle"}, map[string]string{"WatchSession": "cache-token", "GetSessionMessage": "turn-token"}},
		{"stop", []string{"stop", "das_1"}, map[string]string{"InterruptSession": "cache-token"}},
		{"stop --archive", []string{"stop", "das_1", "--archive"}, map[string]string{"ArchiveSession": "cache-token"}},
		{"send", []string{"send", "das_1", "hello"}, map[string]string{"SendInput": "cache-token"}},
		{"launch", []string{"launch", "an-agent@v1", "--source", "github:acme/lib", "--message", "hello"}, map[string]string{"Launch": "cache-token"}},
		{"send --watch", []string{"send", "das_1", "hello", "--watch"}, map[string]string{"GetSession": "cache-token", "SendInput": "cache-token", "WatchSession": "cache-token", "GetSessionMessage": "cache-token"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := serveSandbox(t, replied(), "cache-token")
			if _, errOut, err := runAgent(t, tc.args...); err != nil {
				t.Fatalf("%v\n%s", err, errOut)
			}
			if got := seen(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("credentials by RPC = %v, want %v", got, tc.want)
			}
		})
	}
}

// With no cache token the calls that take a turn's token still work, and the rest fail before anything is sent.
func TestATurnTokenAloneMakesOnlyTheCallsThatTakeIt(t *testing.T) {
	seen := serveSandbox(t, &fakeAgent{}, "")
	if _, _, err := runAgent(t, "list"); err != nil {
		t.Fatal(err)
	}
	if got := seen(); !reflect.DeepEqual(got, map[string]string{"ListDepotAgentSessions": "turn-token"}) {
		t.Fatalf("credentials by RPC = %v", got)
	}
	for _, args := range [][]string{{"create", "hello", "--watch"}, {"watch", "das_1"}, {"stop", "das_1"}, {"stop", "das_1", "--archive"}, {"send", "das_1", "hello"}, {"launch", "an-agent@v1", "--source", "github:acme/lib", "--message", "hello"}} {
		_, _, err := runAgent(t, args...)
		if err == nil || !strings.Contains(err.Error(), "missing API token") {
			t.Fatalf("%v: err = %v, want a missing token", args, err)
		}
		if got := seen(); len(got) != 0 {
			t.Fatalf("%v sent %v before failing", args, got)
		}
	}
}
