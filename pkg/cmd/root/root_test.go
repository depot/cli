package root

import (
	"bytes"
	"encoding/base64"
	"errors"
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
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Manage merge requests") {
		t.Fatal("merge requests appeared in public help")
	}
	for _, name := range []string{"create", "view", "merge", "approve", "comment"} {
		found, remaining, err := cmd.Find([]string{"mr", name})
		if err != nil || len(remaining) != 0 || found.Name() != name {
			t.Fatalf("mr %s is unavailable: %v", name, err)
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
