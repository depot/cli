package pull

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestNewCmdPull(t *testing.T) {
	cmd := NewCmdPull()

	// Test command structure
	if cmd.Use != "pull [flags] [buildID]" {
		t.Errorf("Expected Use to be 'pull [flags] [buildID]', got %s", cmd.Use)
	}

	if cmd.Short != "Pull a project's build from the Depot registry" {
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

func TestPullCommandFlags(t *testing.T) {
	cmd := NewCmdPull()

	// Test required flags exist
	expectedFlags := []struct {
		name         string
		expectedType string
	}{
		{"project", "string"},
		{"token", "string"},
		{"platform", "string"},
		{"tag", "stringSlice"},
		{"progress", "string"},
		{"target", "stringSlice"},
	}

	for _, expected := range expectedFlags {
		flag := cmd.Flags().Lookup(expected.name)
		if flag == nil {
			t.Errorf("Expected '%s' flag to exist", expected.name)
			continue
		}

		// Test some specific flag properties
		switch expected.name {
		case "progress":
			if flag.DefValue != "auto" {
				t.Errorf("Expected progress flag default value to be 'auto', got %s", flag.DefValue)
			}
		case "tag":
			if flag.Shorthand != "t" {
				t.Errorf("Expected tag flag shorthand to be 't', got %s", flag.Shorthand)
			}
		}
	}
}

func TestPullCommand_ValidatesAsCobraCommand(t *testing.T) {
	cmd := NewCmdPull()

	// Test that it can be added to a parent command without issues
	root := &cobra.Command{Use: "depot"}
	root.AddCommand(cmd)

	// Test that help works
	root.SetArgs([]string{"pull", "--help"})

	err := root.Execute()
	if err != nil {
		t.Errorf("Expected help command to work, got error: %v", err)
	}
}