package root

import "testing"

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
