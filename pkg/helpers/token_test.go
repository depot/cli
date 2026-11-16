package helpers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/depot/cli/pkg/runtoken"
	"github.com/spf13/viper"
)

func withRunTokenServer(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"value":"run-svid"}`)
	}))
	t.Cleanup(server.Close)
	source := runtoken.New(runtoken.EnvURL, server.URL)
	previous := runTokenSource
	runTokenSource = func() *runtoken.Source { return source }
	t.Cleanup(func() { runTokenSource = previous })
}

func TestRunTokenPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, flag, env, config, cache string
		runToken                       bool
		want                           string
	}{
		{name: "flag wins", flag: "flag-token", env: "env-token", runToken: true, want: "flag-token"},
		{name: "DEPOT_TOKEN wins", env: "env-token", runToken: true, want: "env-token"},
		{name: "config wins", config: "config-token", runToken: true, want: "config-token"},
		{name: "run token beats cache token", cache: "cache-token", runToken: true, want: "run-svid"},
		{name: "no endpoint keeps old behaviour", cache: "cache-token", want: "cache-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DEPOT_TOKEN", tc.env)
			t.Setenv("DEPOT_JIT_TOKEN", "")
			t.Setenv("DEPOT_CACHE_TOKEN", tc.cache)
			viper.Set("api_token", tc.config)
			t.Cleanup(func() { viper.Set("api_token", "") })
			previousRun, previousTurn := runTokenSource, turnTokenSource
			runTokenSource = func() *runtoken.Source { return nil }
			turnTokenSource = runTokenSource
			t.Cleanup(func() { runTokenSource, turnTokenSource = previousRun, previousTurn })
			if tc.runToken {
				withRunTokenServer(t)
			}

			if got := ResolveStaticOrgAuth(tc.flag); got != tc.want {
				t.Fatalf("ResolveStaticOrgAuth = %q, want %q", got, tc.want)
			}
			if tc.cache == "" {
				if got, _ := ResolveProjectAuth(context.Background(), tc.flag); got != tc.want {
					t.Fatalf("ResolveProjectAuth = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func TestResolveCacheAuthKeepsCacheToken(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "")
	t.Setenv("DEPOT_JIT_TOKEN", "")
	t.Setenv("DEPOT_CACHE_TOKEN", "cache-token")
	viper.Set("api_token", "")
	withRunTokenServer(t)

	if got, _ := ResolveOrgAuth(context.Background(), ""); got != "run-svid" {
		t.Fatalf("ResolveOrgAuth = %q, want the run token", got)
	}
	if got, _ := ResolveCacheAuth(context.Background(), ""); got != "cache-token" {
		t.Fatalf("ResolveCacheAuth = %q, want the cache token", got)
	}

	t.Setenv("DEPOT_CACHE_TOKEN", "")
	if got, _ := ResolveCacheAuth(context.Background(), ""); got != "run-svid" {
		t.Fatalf("ResolveCacheAuth without a cache token = %q, want the run token", got)
	}
}

// In a Dispatch sandbox the turn token stands where the cache token stood for org and session commands (DEP-8673).
// Project commands and cache tools keep what they resolved before.
func TestAPITokenURL(t *testing.T) {
	t.Setenv("DEPOT_JIT_TOKEN", "")
	t.Setenv("DEPOT_CACHE_TOKEN", "cache-token")
	viper.Set("api_token", "")
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(w, "no identity", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"value":"turn-svid"}`)
	}))
	t.Cleanup(server.Close)
	previousRun, previousTurn := runTokenSource, turnTokenSource
	runTokenSource = func() *runtoken.Source { return nil }
	turnTokenSource = func() *runtoken.Source { return runtoken.New(runtoken.EnvAPIURL, server.URL) }
	t.Cleanup(func() { runTokenSource, turnTokenSource = previousRun, previousTurn })
	ctx := context.Background()

	t.Setenv("DEPOT_TOKEN", "")
	agentAuth := func(flag string) string {
		token, turn, _ := ResolveAgentAuth(ctx, flag)
		return fmt.Sprintf("%s %v", token, turn)
	}
	if got := agentAuth(""); got != "turn-svid true" {
		t.Fatalf("ResolveAgentAuth = %q, want the turn token ahead of the cache token", got)
	}
	if got, _ := ResolveOrgAuth(ctx, ""); got != "turn-svid" {
		t.Fatalf("ResolveOrgAuth = %q, want the turn token ahead of the cache token", got)
	}
	if got := ResolveStaticOrgAuth(""); got != "turn-svid" {
		t.Fatalf("ResolveStaticOrgAuth = %q, want the turn token ahead of the cache token", got)
	}
	if got, _ := ResolveCacheAuth(ctx, ""); got != "cache-token" {
		t.Fatalf("ResolveCacheAuth = %q, want the cache token as before", got)
	}
	if got, _ := ResolveProjectAuth(ctx, ""); got != "cache-token" {
		t.Fatalf("ResolveProjectAuth = %q, want the cache token as before", got)
	}
	if got := agentAuth("flag-token"); got != "flag-token false" {
		t.Fatalf("ResolveAgentAuth with a flag = %q", got)
	}
	if got, _ := ResolveOrgAuth(ctx, "flag-token"); got != "flag-token" {
		t.Fatalf("ResolveOrgAuth with a flag = %q", got)
	}
	t.Setenv("DEPOT_TOKEN", "env-token")
	if got := agentAuth(""); got != "env-token false" {
		t.Fatalf("ResolveAgentAuth with DEPOT_TOKEN = %q", got)
	}
	if got, _ := ResolveOrgAuth(ctx, ""); got != "env-token" {
		t.Fatalf("ResolveOrgAuth with DEPOT_TOKEN = %q", got)
	}
	t.Setenv("DEPOT_TOKEN", "")
	viper.Set("api_token", "config-token")
	if got, _ := ResolveOrgAuth(ctx, ""); got != "config-token" {
		t.Fatalf("ResolveOrgAuth with a saved login = %q", got)
	}
	viper.Set("api_token", "")
	withRunTokenServer(t)
	if got := agentAuth(""); got != "run-svid false" {
		t.Fatalf("ResolveAgentAuth with both URLs = %q, want the run token", got)
	}
	if got, _ := ResolveOrgAuth(ctx, ""); got != "run-svid" {
		t.Fatalf("ResolveOrgAuth with both URLs = %q, want the run token", got)
	}
	runTokenSource = func() *runtoken.Source { return nil }

	fail = true
	if got := agentAuth(""); got != "cache-token false" {
		t.Fatalf("ResolveAgentAuth with no turn token to fetch = %q, want the cache token", got)
	}
	if got, _ := ResolveOrgAuth(ctx, ""); got != "cache-token" {
		t.Fatalf("ResolveOrgAuth with no turn token to fetch = %q, want the cache token", got)
	}
	t.Setenv("DEPOT_CACHE_TOKEN", "")
	stderr := os.Stderr
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = write
	got, turn, _ := ResolveAgentAuth(ctx, "")
	os.Stderr = stderr
	write.Close()
	printed, _ := io.ReadAll(read)
	if got != "" || turn || !strings.Contains(string(printed), "error: could not fetch a token from DEPOT_API_TOKEN_URL") {
		t.Fatalf("got token %q and stderr %q", got, printed)
	}
}
