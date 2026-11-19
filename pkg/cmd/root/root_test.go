package root

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func TestRootRegistersTestsCommand(t *testing.T) {
	cmd := NewCmdRoot("test-version", "test-date")
	for _, child := range cmd.Commands() {
		if child.Name() == "tests" {
			return
		}
	}
	t.Fatal("expected root command to register tests command")
}

func TestRootRegistersGitHubActionsJobs(t *testing.T) {
	for _, path := range [][]string{{"jobs"}, {"jobs", "analytics"}, {"list", "jobs"}} {
		cmd := NewCmdRoot("test-version", "test-date")
		found, remaining, err := cmd.Find(path)
		if err != nil || len(remaining) != 0 || found.Name() != path[len(path)-1] {
			t.Fatalf("command %v not registered: found %v, remaining %v, error %v", path, found, remaining, err)
		}
	}
}

func TestMergeRequestsAreHiddenButDirectlyAvailable(t *testing.T) {
	cmd := NewCmdRoot("test-version", "test-date")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Manage merge requests") {
		t.Fatal("merge requests appeared in public help")
	}

	out.Reset()
	cmd.SetArgs([]string{"mr", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"create", "view", "merge", "approve", "comment"} {
		if !strings.Contains(out.String(), "  "+name+" ") {
			t.Errorf("mr help does not list %s", name)
		}
	}
}

func TestRetiredClaudeIsHidden(t *testing.T) {
	cmd := NewCmdRoot("test-version", "test-date")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	for _, args := range [][]string{{"--help"}, {"__complete", ""}} {
		out.Reset()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "claude") {
			t.Fatalf("%v lists claude:\n%s", args, out.String())
		}
	}

	claude, remaining, err := cmd.Find([]string{"claude"})
	if err != nil || len(remaining) != 0 || claude.Name() != "claude" {
		t.Fatalf("claude command not registered: %v, %v, %v", claude, remaining, err)
	}

	out.Reset()
	cmd.SetArgs([]string{"help", "claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "depot claude has been retired; use Depot Managed Agents instead") {
		t.Errorf("help has no retirement notice: %q", out.String())
	}
}

func TestClaudeRejectsLegacyInvocationsWithoutCallingAPI(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusServiceUnavailable)
	}), &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	t.Setenv("DEPOT_TOKEN", "test-token")
	t.Setenv("DEPOT_ORG_ID", "j1x843hgzl")

	for _, sandboxID := range []string{"", "sandbox-1"} {
		t.Run("sandbox="+sandboxID, func(t *testing.T) {
			t.Setenv("DEPOT_SANDBOX_ID", sandboxID)
			for _, args := range [][]string{
				{},
				{"--local"},
				{"--resume", "session-1"},
				{"--local", "--resume=session-1"},
				{"--wait", "--resume", "session-1"},
				{"--repository", "https://github.com/depot/cli.git", "-p", "hello"},
				{"--session-id=session-1", "--model", "claude"},
				{"--help"},
				{"-h"},
				{"list-sessions", "--output", "json"},
				{"secrets"},
				{"secrets", "add", "EXAMPLE", "--value", "example"},
				{"secrets", "list"},
				{"secrets", "ls"},
				{"secrets", "remove", "EXAMPLE", "--force"},
				{"secrets", "rm", "EXAMPLE", "--force"},
				{"secrets", "delete", "EXAMPLE", "--force"},
				{"secrets", "del", "EXAMPLE", "--force"},
			} {
				t.Run(strings.Join(args, " "), func(t *testing.T) {
					cmd := NewCmdRoot("test-version", "test-date")
					var stdout, stderr bytes.Buffer
					cmd.SetOut(&stdout)
					cmd.SetErr(&stderr)
					cmd.SetArgs(append([]string{"claude"}, args...))
					err := cmd.Execute()
					const want = "depot claude has been retired; use Depot Managed Agents instead: https://depot.dev/docs/managed-agents/claude-code-self-hosted"
					if err == nil || err.Error() != want {
						t.Fatalf("error = %v, want %q", err, want)
					}
					if stdout.Len() != 0 || !strings.Contains(stderr.String(), want) {
						t.Errorf("retirement must only print to stderr: stdout=%q, stderr=%q", stdout.String(), stderr.String())
					}
				})
			}
		})
	}
	if count := requests.Load(); count != 0 {
		t.Errorf("retired command sent %d API requests", count)
	}
}

func TestRetiringClaudePreservesAgentAndSandboxCommands(t *testing.T) {
	for _, args := range [][]string{{"agent", "launch"}, {"sandbox", "exec"}, {"sandbox", "exec-pipe"}, {"sandbox", "pty"}, {"checks", "agents"}} {
		cmd := NewCmdRoot("test-version", "test-date")
		found, remaining, err := cmd.Find(args)
		if err != nil || len(remaining) != 0 || found.Name() != args[len(args)-1] {
			t.Errorf("command %v not registered: found %v, remaining %v, error %v", args, found, remaining, err)
		}
	}
}

func TestBuildkitRegistryProxyIsHiddenAndRunsInternalCommand(t *testing.T) {
	t.Setenv("CA_CERT", "invalid-base64!")
	cmd := NewCmdRoot("test-version", "test-date")
	proxy, remaining, err := cmd.Find([]string{"buildkit-registry-proxy"})
	if err != nil || len(remaining) != 0 || proxy.Name() != "buildkit-registry-proxy" || !proxy.Hidden {
		t.Fatalf("hidden proxy command not registered: %v, %v, %v", proxy, remaining, err)
	}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "buildkit-registry-proxy") {
		t.Fatal("internal proxy is visible in public help")
	}
	cmd.SetArgs([]string{"buildkit-registry-proxy"})
	err = cmd.Execute()
	var encodingErr base64.CorruptInputError
	if !errors.As(err, &encodingErr) {
		t.Fatalf("command did not parse the internal proxy configuration: %v", err)
	}
}
