package ci

import "testing"

func TestNewCmdCI(t *testing.T) {
	cmd := NewCmdCI()

	if cmd.Use != "ci" {
		t.Errorf("expected Use='ci', got '%s'", cmd.Use)
	}

	subcommands := cmd.Commands()
	if len(subcommands) != 5 {
		t.Errorf("expected 5 subcommands, got %d", len(subcommands))
	}

	expectedNames := map[string]bool{
		"migrate": false,
		"secrets": false,
		"vars":    false,
		"status":  false,
		"logs":    false,
	}

	for _, subcmd := range subcommands {
		if _, exists := expectedNames[subcmd.Use]; exists {
			expectedNames[subcmd.Use] = true
		} else {
			t.Errorf("unexpected subcommand: %s", subcmd.Use)
		}
	}

	for name, found := range expectedNames {
		if !found {
			t.Errorf("missing subcommand: %s", name)
		}
	}
}
