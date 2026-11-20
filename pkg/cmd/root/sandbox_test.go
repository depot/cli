package root

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/viper"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// serveSandbox is the API as a Dispatch sandbox reaches it: a turn token URL and a cache token, no login.
// It answers every call not_found and returns the credentials the calls since the last read arrived with.
func serveSandbox(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]bool{}
	api := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] = true
		mu.Unlock()
		http.NotFound(w, r)
	}), &http2.Server{}))
	t.Cleanup(api.Close)
	turn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"value":"turn-token"}`)
	}))
	t.Cleanup(turn.Close)
	saved := viper.GetString("api_token")
	viper.Set("api_token", "")
	t.Cleanup(func() { viper.Set("api_token", saved) })
	for name, value := range map[string]string{
		"DEPOT_API_URL": api.URL, "DEPOT_API_TOKEN_URL": turn.URL, "DEPOT_CACHE_TOKEN": "cache-token",
		"DEPOT_TOKEN": "", "DEPOT_JIT_TOKEN": "", "DEPOT_RUN_TOKEN_URL": "", "DEPOT_ORG_ID": "org-1",
	} {
		t.Setenv(name, value)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		got := slices.Sorted(maps.Keys(seen))
		seen = map[string]bool{}
		return got
	}
}

// In a sandbox the CI, Agent Checks and merge request read commands call as the turn, and a merge request change is
// refused before anything is sent. Every other command sends what it did before.
func TestSandboxCredentialByCommand(t *testing.T) {
	seen := serveSandbox(t)
	for _, tc := range []struct{ command, want string }{
		{"ci status run-1", "turn-token"},
		{"ci logs run-1", "turn-token"},
		{"ci summary attempt-1", "turn-token"},
		{"ci metrics attempt-1", "turn-token"},
		{"ci steps attempt-1", "turn-token"},
		{"ci diagnose --run run-1", "turn-token"},
		{"ci run list", "turn-token"},
		{"ci run show run-1", "turn-token"},
		{"ci workflow show workflow-1", "turn-token"},
		{"ci workflow list", "turn-token"},
		{"ci artifacts list run-1", "turn-token"},
		{"ci artifacts download artifact-1", "turn-token"},
		{"checks list", "turn-token"},
		{"checks get check-1", "turn-token"},
		{"checks attempts check-1", "turn-token"},
		{"checks transcript check-1", "turn-token"},

		{"ci cancel run-1", "cache-token"},
		{"ci rerun run-1", "cache-token"},
		{"ci retry run-1 --failed", "cache-token"},
		{"ci secrets list", "cache-token"},
		{"ci vars list", "cache-token"},
		{"checks retry check-1", "cache-token"},
		{"checks resolve check-1 --all", "cache-token"},
		{"checks agents list", "cache-token"},
		{"agent stop das_1", "cache-token"},
		{"agent stop das_1 --archive", "cache-token"},

		{"mr view feature --repo repo-1", "turn-token"},
		{"mr view --id mr-1", "turn-token"},
		{"mr viewer", "turn-token"},
		{"mr list --repo repo-1", "turn-token"},
		{"mr threads feature --repo repo-1", "turn-token"},

		{"mr create --repo repo-1 --head feature --title t", ""},
		{"mr edit feature --repo repo-1 --title t", ""},
		{"mr close feature --repo repo-1", ""},
		{"mr reopen feature --repo repo-1", ""},
		{"mr comment feature --repo repo-1 --body b", ""},
		{"mr reply comment-1 --body b", ""},
		{"mr resolve comment-1", ""},
		{"mr unresolve comment-1", ""},
		{"mr approve feature --repo repo-1", ""},
	} {
		t.Run(tc.command, func(t *testing.T) {
			cmd := NewCmdRoot("test-version", "test-date")
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(strings.Fields(tc.command))
			err := cmd.Execute()
			if tc.want == "" {
				if got := seen(); err == nil || !strings.Contains(err.Error(), "cannot be made as an agent turn") || len(got) != 0 {
					t.Fatalf("err = %v after sending %v, want the agent turn refusal and nothing sent", err, got)
				}
				return
			}
			if got := seen(); !slices.Equal(got, []string{tc.want}) {
				t.Fatalf("credentials sent = %v, want only %q (err: %v)", got, tc.want, err)
			}
		})
	}
}

// Outside the sandbox shape (a turn token URL and nothing explicit) the merge request commands are unchanged.
func TestMergeRequestCommandsOutsideAnAgentTurn(t *testing.T) {
	run := func(t *testing.T, command string) error {
		t.Helper()
		cmd := NewCmdRoot("test-version", "test-date")
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(strings.Fields(command))
		return cmd.Execute()
	}
	writes := []string{"mr comment feature --repo repo-1 --body b", "mr approve feature --repo repo-1", "mr resolve comment-1"}
	for _, tc := range []struct{ name, tokenURL, env, flag, want string }{
		{name: "cache token only", want: "cache-token"},
		{name: "DEPOT_TOKEN beside a token URL", tokenURL: "keep", env: "env-token", want: "env-token"},
		{name: "--token beside a token URL", tokenURL: "keep", flag: " --token flag-token", want: "flag-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := serveSandbox(t)
			if tc.tokenURL == "" {
				t.Setenv("DEPOT_API_TOKEN_URL", "")
			}
			t.Setenv("DEPOT_TOKEN", tc.env)
			for _, command := range append(writes, "mr list --repo repo-1") {
				err := run(t, command+tc.flag)
				if got := seen(); !slices.Equal(got, []string{tc.want}) {
					t.Fatalf("%s sent %v, want only %q (err: %v)", command, got, tc.want, err)
				}
			}
		})
	}

	seen := serveSandbox(t)
	err := run(t, "mr list")
	if got := seen(); err == nil || !strings.Contains(err.Error(), "--repo is required in an agent session") || len(got) != 0 {
		t.Fatalf("err = %v after sending %v, want --repo asked for and nothing sent", err, got)
	}
}

// Only these files may name a turn token resolver, this many times each. A new use fails here until it is listed.
func TestOnlyListedCommandsResolveATurnToken(t *testing.T) {
	want := map[string]int{
		"agent/session.go":     1,
		"checks/checks.go":     1,
		"checks/executions.go": 3,
		"checks/transcript.go": 1,
		"ci/artifacts.go":      2,
		"ci/diagnose.go":       1,
		"ci/logs.go":           1,
		"ci/metrics.go":        1,
		"ci/run.go":            1,
		"ci/run_show.go":       1,
		"ci/status.go":         1,
		"ci/steps.go":          1,
		"ci/summary.go":        1,
		"ci/workflow.go":       2,
		"mr/mr.go":             1,
	}
	turnCalls := []string{"ResolveTurnReadAuth", "ResolveAgentAuth", "authenticateRead", "Turn"}
	got := map[string]int{}
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if sel, ok := node.(*ast.SelectorExpr); ok && slices.Contains(turnCalls, sel.Sel.Name) {
				if pkg, ok := sel.X.(*ast.Ident); sel.Sel.Name != "Turn" || (ok && pkg.Name == "runtoken") {
					got[filepath.ToSlash(strings.TrimPrefix(path, "../"))]++
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, want) {
		t.Fatalf("turn token callers = %v, want %v", got, want)
	}
}
