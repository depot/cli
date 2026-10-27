package helpers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	source := runtoken.New(server.URL)
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
