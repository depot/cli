package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"slices"
	"strconv"
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
// It returns the credential each RPC was called with. The RPCs in old refuse a turn's token, as an older API does.
func serveSandbox(t *testing.T, f *fakeAgent, cacheToken string, old ...string) func() map[string]string {
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
		rpc, token := path.Base(r.URL.Path), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen[rpc] = token
		mu.Unlock()
		if token == "turn-token" && slices.Contains(old, rpc) {
			_ = connect.NewErrorWriter().Write(w, r, connect.NewError(connect.CodePermissionDenied, errors.New(notYet)))
			return
		}
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

const notYet = "This Depot API call can't be made as a Depot agent turn's sender yet"

// In a sandbox every session RPC but the two of stop is made with the turn's token, with or without a cache token.
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
	launch := []string{"launch", "an-agent@v1", "--source", "github:acme/lib", "--message", "hello"}
	for _, tc := range []struct {
		name string
		args []string
		want map[string]string
	}{
		{"create", []string{"create", "hello"}, map[string]string{"CreateSession": "turn-token"}},
		{"create --watch", []string{"create", "hello", "--watch"}, map[string]string{"CreateSession": "turn-token", "WatchSession": "turn-token", "GetSessionMessage": "turn-token"}},
		{"list", []string{"list"}, map[string]string{"ListDepotAgentSessions": "turn-token"}},
		{"logs", []string{"logs", "das_1"}, map[string]string{"GetSession": "turn-token", "GetSessionMessage": "turn-token"}},
		{"watch", []string{"watch", "das_1", "--until-idle"}, map[string]string{"WatchSession": "turn-token", "GetSessionMessage": "turn-token"}},
		{"stop", []string{"stop", "das_1"}, map[string]string{"InterruptSession": "cache-token"}},
		{"stop --archive", []string{"stop", "das_1", "--archive"}, map[string]string{"ArchiveSession": "cache-token"}},
		{"send", []string{"send", "das_1", "hello"}, map[string]string{"SendInput": "turn-token"}},
		{"send --watch", []string{"send", "das_1", "hello", "--watch"}, map[string]string{"GetSession": "turn-token", "SendInput": "turn-token", "WatchSession": "turn-token", "GetSessionMessage": "turn-token"}},
		{"launch", launch, map[string]string{"Launch": "turn-token"}},
		{"launch --watch", append(launch, "--watch"), map[string]string{"Launch": "turn-token", "WatchSession": "turn-token", "GetSessionMessage": "turn-token"}},
	} {
		for _, cacheToken := range []string{"cache-token", ""} {
			t.Run(tc.name+" with cache token "+strconv.Quote(cacheToken), func(t *testing.T) {
				seen := serveSandbox(t, replied(), cacheToken)
				_, errOut, err := runAgent(t, tc.args...)
				// stop has no call that takes a turn's token, so without a cache token it fails before sending.
				if cacheToken == "" && tc.args[0] == "stop" {
					if got := seen(); err == nil || !strings.Contains(err.Error(), "missing API token") || len(got) != 0 {
						t.Fatalf("err = %v after sending %v, want a missing token and nothing sent", err, got)
					}
					return
				}
				if err != nil {
					t.Fatalf("%v\n%s", err, errOut)
				}
				if got := seen(); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("credentials by RPC = %v, want %v", got, tc.want)
				}
			})
		}
	}
}

// An API from before DEP-8577 refuses the turn's token on these calls. The error names the call, and when a
// session was already created it says so and how to read it.
func TestAnAPIThatRefusesTheTurnTokenIsNamedAndLeavesNothingUnsaid(t *testing.T) {
	launch := []string{"launch", "an-agent@v1", "--source", "github:acme/lib", "--message", "hello"}
	for _, tc := range []struct {
		name string
		args []string
		rpc  string
		did  string
		want map[string]string
	}{
		{"watch", []string{"watch", "das_1", "--until-idle"}, "WatchSession", "", map[string]string{"WatchSession": "turn-token"}},
		{"create --watch", []string{"create", "hello", "--watch", "-o", "json"}, "WatchSession", "Session das_1 was created", map[string]string{"CreateSession": "turn-token", "WatchSession": "turn-token"}},
		{"send", []string{"send", "das_1", "hello"}, "SendInput", "", map[string]string{"SendInput": "turn-token"}},
		{"send --watch", []string{"send", "das_1", "hello", "--watch"}, "SendInput", "", map[string]string{"GetSession": "turn-token", "SendInput": "turn-token"}},
		{"launch", launch, "Launch", "", map[string]string{"Launch": "turn-token"}},
		{"launch --watch", append(launch, "--watch"), "Launch", "", map[string]string{"Launch": "turn-token"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAgent{got: &agentv1.GetSessionResponse{Session: &agentv1.DepotAgentSession{SessionId: "das_1", Status: "idle"}}}
			seen := serveSandbox(t, f, "cache-token", "WatchSession", "SendInput", "Launch")
			out, _, err := runAgent(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), notYet) || !strings.Contains(err.Error(), "agent turn's token. The API takes it on "+tc.rpc+" only") || !strings.Contains(err.Error(), "pass --token or set DEPOT_TOKEN") {
				t.Fatalf("err = %v, want the refusal naming %s", err, tc.rpc)
			}
			if strings.Contains(err.Error(), "turn-token") || strings.Contains(out, `"outcome"`) {
				t.Fatalf("err = %v, out = %q: want no token and no result", err, out)
			}
			if said := strings.Contains(err.Error(), "is not being watched; read its reply with `depot agent logs das_1`"); said != (tc.did != "") || !strings.Contains(err.Error(), tc.did) {
				t.Fatalf("err = %v, want %q said", err, tc.did)
			}
			if got := seen(); !reflect.DeepEqual(got, tc.want) || f.watchCount() != 0 {
				t.Fatalf("credentials by RPC = %v, want %v and no watch reaching the API", got, tc.want)
			}
		})
	}
}
