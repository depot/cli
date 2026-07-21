package pull

import (
	"strings"
	"testing"
)

func TestPullRejectsEmptyBuildIDBeforeExecution(t *testing.T) {
	cmd := NewCmdPull()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{""})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for an empty build ID")
	}
	if !strings.Contains(err.Error(), "build ID or tag must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}
