package root

import (
	"bytes"
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

func TestRootHelpLeavesOutDepotAgent(t *testing.T) {
	cmd := NewCmdRoot("test-version", "test-date")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if name := strings.Fields(line + " ."); name[0] == "agent" || name[0] == "send" {
			t.Fatalf("depot --help lists %q until it is announced (DEP-7794):\n%s", name[0], out.String())
		}
	}
}
