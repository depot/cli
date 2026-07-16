package root

import "testing"

func TestRootRegistersCommands(t *testing.T) {
	cmd := NewCmdRoot("test-version", "test-date")
	registered := map[string]bool{}
	for _, child := range cmd.Commands() {
		registered[child.Name()] = true
	}

	for _, name := range []string{"browse", "tests"} {
		if !registered[name] {
			t.Errorf("expected root command to register %s command", name)
		}
	}
}
