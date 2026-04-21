package ci

import (
	"bytes"
	"strings"
	"testing"
)

func TestRerunFlagValidation(t *testing.T) {
	t.Run("missing run-id", func(t *testing.T) {
		cmd := NewCmdRerun()
		cmd.SetArgs([]string{})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SilenceUsage = true
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "accepts 1 arg") {
			t.Fatalf("expected args error, got %v", err)
		}
	})
}

func TestCICommandRegistration(t *testing.T) {
	cmd := NewCmdCI()
	wanted := []string{"cancel", "dispatch", "rerun", "retry"}
	for _, name := range wanted {
		var found bool
		for _, sub := range cmd.Commands() {
			if sub.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("subcommand %q not registered under `depot ci`", name)
		}
	}
}
