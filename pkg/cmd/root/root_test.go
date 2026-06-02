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
