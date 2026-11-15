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
			previous := runTokenSource
			runTokenSource = func() *runtoken.Source { return nil }
			t.Cleanup(func() { runTokenSource = previous })
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

// In a Dispatch sandbox only session commands use the turn token: every other command keeps what it resolved before.
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
	if org, turn, _ := ResolveAgentAuth(ctx, "", true); org != "cache-token" || turn != "turn-svid" {
		t.Fatalf("ResolveAgentAuth = %q, %q, want the cache token as before and the turn token beside it", org, turn)
	}
	if got, _ := ResolveOrgAuth(ctx, ""); got != "cache-token" {
		t.Fatalf("ResolveOrgAuth = %q, want the cache token as before", got)
	}
	if got, _ := ResolveProjectAuth(ctx, ""); got != "cache-token" {
		t.Fatalf("ResolveProjectAuth = %q, want the cache token as before", got)
	}
	if org, turn, _ := ResolveAgentAuth(ctx, "flag-token", true); org != "flag-token" || turn != "flag-token" {
		t.Fatalf("ResolveAgentAuth with a flag = %q, %q", org, turn)
	}
	t.Setenv("DEPOT_TOKEN", "env-token")
	if org, turn, _ := ResolveAgentAuth(ctx, "", true); org != "env-token" || turn != "env-token" {
		t.Fatalf("ResolveAgentAuth with DEPOT_TOKEN = %q, %q", org, turn)
	}
	t.Setenv("DEPOT_TOKEN", "")
	withRunTokenServer(t)
	if org, turn, _ := ResolveAgentAuth(ctx, "", true); org != "run-svid" || turn != "run-svid" {
		t.Fatalf("ResolveAgentAuth with both URLs = %q, %q, want the run token", org, turn)
	}
	runTokenSource = func() *runtoken.Source { return nil }

	t.Setenv("DEPOT_CACHE_TOKEN", "")
	if org, turn, _ := ResolveAgentAuth(ctx, "", true); org != "" || turn != "turn-svid" {
		t.Fatalf("ResolveAgentAuth with the turn URL alone = %q, %q, want no org token", org, turn)
	}
	fail = true
	t.Setenv("DEPOT_CACHE_TOKEN", "")
	stderr := os.Stderr
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = write
	got, turn, _ := ResolveAgentAuth(ctx, "", false)
	os.Stderr = stderr
	write.Close()
	printed, _ := io.ReadAll(read)
	if got != "" || turn != "" || !strings.Contains(string(printed), "error: could not fetch a token from DEPOT_API_TOKEN_URL") {
		t.Fatalf("got token %q and stderr %q", got, printed)
	}
}
