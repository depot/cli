package sandbox

import (
	"strings"
	"testing"
)

// Confirms every verb is registered on the root command. This catches the
// wiring mistake where a verb file exists but sandbox.go forgets to call
// AddCommand for it.
func TestSandboxVerbTree(t *testing.T) {
	root := NewCmdSandbox()

	want := []string{"create", "exec", "stop", "kill"}
	got := map[string]bool{}
	for _, c := range root.Commands() {
		// A cobra Use string can include a "[flags] <args...>" suffix; the
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
