package root

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
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

func TestClaudeIsHiddenDeprecatedAndStillRuns(t *testing.T) {
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

	// Sandboxes and scripts read stdout, so the notice must only reach the real stderr.
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	cmd = NewCmdRoot("test-version", "test-date")
	cmd.SetArgs([]string{"claude", "secrets"})
	runErr := cmd.Execute()
	os.Stdout, os.Stderr = origOut, origErr
	outW.Close()
	errW.Close()
	stdout, _ := io.ReadAll(outR)
	stderr, _ := io.ReadAll(errR)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(string(stderr), "use Depot Managed Agents instead") {
		t.Errorf("stderr has no deprecation notice: %q", stderr)
	}
	if strings.Contains(string(stdout), "deprecated") || !strings.Contains(string(stdout), "Manage secrets") {
		t.Errorf("stdout changed: %q", stdout)
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
