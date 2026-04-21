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
