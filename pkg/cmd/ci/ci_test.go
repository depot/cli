package ci

import (
	"testing"
)

// TestCICommandRegistration guards `depot ci`'s subcommand surface so a future
// refactor doesn't accidentally drop a verb. New verbs should be added to the
// `wanted` slice below.
func TestCICommandRegistration(t *testing.T) {
	cmd := NewCmdCI()

	wanted := []string{
		// inspection / interactive
		"run",
		"status",
		"diagnose",
		"logs",
		"summary",
		"ssh",
		// mutation verbs (added in DEP-4221)
		"cancel",
		"dispatch",
		"rerun",
		"retry",
		// project resources
		"secrets",
		"vars",
		// migration helper
		"migrate",
	}

	registered := map[string]bool{}
	for _, sub := range cmd.Commands() {
		registered[sub.Name()] = true
	}

	for _, name := range wanted {
		if !registered[name] {
			t.Errorf("subcommand %q not registered under `depot ci`", name)
		}
	}
}
