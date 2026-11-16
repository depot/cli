package checks

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	checksv1 "github.com/depot/cli/pkg/proto/depot/checks/v1"
	"github.com/depot/cli/pkg/proto/depot/checks/v1/checksv1connect"
	"github.com/spf13/viper"
)

// A Dispatch sandbox has a turn token URL and a cache token and no login: the API is called as the turn (DEP-8673).
func TestSandboxCallsTheAPIAsTheTurn(t *testing.T) {
	turn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"value":"turn-token"}`)
	}))
	t.Cleanup(turn.Close)
	saved := viper.GetString("api_token")
	viper.Set("api_token", "")
	t.Cleanup(func() { viper.Set("api_token", saved) })
	for name, value := range map[string]string{
		"DEPOT_API_TOKEN_URL": turn.URL, "DEPOT_CACHE_TOKEN": "cache-token",
		"DEPOT_TOKEN": "", "DEPOT_JIT_TOKEN": "", "DEPOT_RUN_TOKEN_URL": "",
	} {
		t.Setenv(name, value)
	}
	var sent string
	path := checksv1connect.CheckServiceGetCheckProcedure
	localAPI(t).Handle(path, connect.NewUnaryHandler(path, func(_ context.Context, req *connect.Request[checksv1.GetCheckRequest]) (*connect.Response[checksv1.GetCheckResponse], error) {
		sent = req.Header().Get("Authorization")
		return connect.NewResponse(&checksv1.GetCheckResponse{Check: &checksv1.Check{Id: "check", CurrentAttempt: fixtureAttempt()}}), nil
	}))

	run := func(args ...string) {
		t.Helper()
		cmd := NewCmdChecks()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	run("get", "check")
	if sent != "Bearer turn-token" {
		t.Fatalf("Authorization = %q, want the turn's token", sent)
	}
	run("get", "check", "--token", "flag-token")
	if sent != "Bearer flag-token" {
		t.Fatalf("Authorization with --token = %q, want the flag's token", sent)
	}
}
