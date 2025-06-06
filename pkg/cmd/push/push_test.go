package push

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestNewCmdPush(t *testing.T) {
	cmd := NewCmdPush()

	// Test command structure
	if cmd.Use != "push [flags] [buildID]" {
		t.Errorf("Expected Use to be 'push [flags] [buildID]', got %s", cmd.Use)
	}

	if cmd.Short != "Push a project's build from the Depot registry to a destination registry" {
		t.Errorf("Expected Short description, got %s", cmd.Short)
	}

	// Test that it accepts max 1 argument
	if cmd.Args == nil {
		t.Error("Expected Args to be set")
	}

	// Test RunE is set
	if cmd.RunE == nil {
		t.Error("Expected RunE to be set")
	}
}

func TestPushCommandFlags(t *testing.T) {
	cmd := NewCmdPush()

	// Test required flags exist
	expectedFlags := []struct {
		name         string
		expectedType string
		shorthand    string
		defValue     string
	}{
		{"project", "string", "", ""},
		{"token", "string", "", ""},
		{"progress", "string", "", "auto"},
		{"tag", "stringArray", "t", "[]"},
		{"target", "string", "", ""},
	}

	for _, expected := range expectedFlags {
		flag := cmd.Flags().Lookup(expected.name)
		if flag == nil {
			t.Errorf("Expected '%s' flag to exist", expected.name)
			continue
		}

		if expected.shorthand != "" && flag.Shorthand != expected.shorthand {
			t.Errorf("Expected %s flag shorthand to be '%s', got '%s'", expected.name, expected.shorthand, flag.Shorthand)
		}

		if expected.defValue != "" && flag.DefValue != expected.defValue {
			t.Errorf("Expected %s flag default value to be '%s', got '%s'", expected.name, expected.defValue, flag.DefValue)
		}
	}
}

func TestPushCommand_ValidatesAsCobraCommand(t *testing.T) {
	cmd := NewCmdPush()

	// Test that it can be added to a parent command without issues
	root := &cobra.Command{Use: "depot"}
	root.AddCommand(cmd)

	// Test that help works
	root.SetArgs([]string{"push", "--help"})

	err := root.Execute()
	if err != nil {
		t.Errorf("Expected help command to work, got error: %v", err)
	}
}
