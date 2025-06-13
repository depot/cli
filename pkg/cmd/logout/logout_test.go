package logout

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
)

func TestNewCmdLogout(t *testing.T) {
	cmd := NewCmdLogout()

	// Test command structure
	if cmd.Use != "logout" {
		t.Errorf("Expected Use to be 'logout', got %s", cmd.Use)
	}

	if cmd.Short != "Clear authentication token" {
		t.Errorf("Expected Short to be 'Clear authentication token', got %s", cmd.Short)
	}

	// Test RunE is set
	if cmd.RunE == nil {
		t.Error("Expected RunE to be set")
	}

	// Test that it has no flags
	if cmd.Flags().NFlag() != 0 {
		t.Errorf("Expected no flags, got %d", cmd.Flags().NFlag())
	}
}

func TestLogoutCommand_ValidatesAsCobraCommand(t *testing.T) {
	cmd := NewCmdLogout()

	// Test that it can be added to a parent command without issues
	root := &cobra.Command{Use: "depot"}
	root.AddCommand(cmd)

	// Test that help works
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"logout", "--help"})

	err := root.Execute()
	if err != nil {
		t.Errorf("Expected help command to work, got error: %v", err)
	}

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("Clear authentication token")) {
		t.Error("Expected help output to contain command description")
	}
}
