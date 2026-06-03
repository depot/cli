package login

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// setToken isolates the api_token viper key (and the DEPOT_API_TOKEN env that
// AutomaticEnv would otherwise read) for the duration of a test so the suite
// never touches the developer's real ~/.config/depot/depot.yaml. The previous
// value is restored on cleanup.
func setToken(t *testing.T, token string) {
	t.Helper()
	t.Setenv("DEPOT_API_TOKEN", token)
	prev := viper.GetString("api_token")
	viper.Set("api_token", token)
	t.Cleanup(func() { viper.Set("api_token", prev) })
}

func TestLoginQuietIsSilentNoOpWhenAlreadyLoggedIn(t *testing.T) {
	setToken(t, "tok-already-here")

	cmd := NewCmdLogin()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--quiet"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("login --quiet returned error: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("login --quiet should produce no stdout, got %q", out.String())
	}
}

func TestLoginPrintsNoticeWhenAlreadyLoggedInWithoutQuiet(t *testing.T) {
	setToken(t, "tok-already-here")

	cmd := NewCmdLogin()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("login returned error: %v", err)
	}
	if !strings.Contains(out.String(), "You are already logged in.") {
		t.Fatalf("expected already-logged-in notice on stdout, got %q", out.String())
	}
}

func TestLoginTokenPrintsStoredToken(t *testing.T) {
	setToken(t, "tok-12345")

	cmd := NewCmdLogin()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"token"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("login token returned error: %v", err)
	}
	if got := strings.TrimRight(out.String(), "\n"); got != "tok-12345" {
		t.Fatalf("login token stdout = %q, want exactly the token", out.String())
	}
}

func TestLoginTokenErrorsWhenNotLoggedIn(t *testing.T) {
	setToken(t, "")

	cmd := NewCmdLogin()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"token"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no token is stored")
	}
	if !strings.Contains(err.Error(), "depot login") {
		t.Fatalf("error = %q, want it to mention `depot login`", err)
	}
	if out.Len() != 0 {
		t.Fatalf("login token should print nothing to stdout when not logged in, got %q", out.String())
	}
}
