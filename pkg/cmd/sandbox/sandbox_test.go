package sandbox

import (
	"strings"
	"testing"
)

// Touch-test: every vertical-slice verb is registered on the root command.
// This catches the wiring regression where a verb file ships but sandbox.go
// forgets the AddCommand.
func TestSandboxVerbTree(t *testing.T) {
	root := NewCmdSandbox()

	want := []string{"create", "exec", "stop", "kill"}
	got := map[string]bool{}
	for _, c := range root.Commands() {
		// cobra command Use can include "[flags] <args...>" suffix; the
		// first whitespace-separated token is the verb name.
		name := c.Use
		if idx := strings.IndexAny(name, " \t"); idx > 0 {
			name = name[:idx]
		}
		got[name] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing verb %q on `depot sandbox`", w)
		}
	}
}
