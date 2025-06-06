package init

import (
	"testing"
)

func TestNewCmdCache(t *testing.T) {
	cmd := NewCmdCache()
	
	// Test command structure
	if cmd.Use != "cache" {
		t.Errorf("Expected Use to be 'cache', got %s", cmd.Use)
	}
	
	if cmd.Short != "Operations for the Depot project cache" {
		t.Errorf("Expected Short description, got %s", cmd.Short)
	}
	
	// Test that it has subcommands
	if !cmd.HasSubCommands() {
		t.Error("Expected cache command to have subcommands")
	}
	
	// Test that reset subcommand exists
	resetCmd, _, err := cmd.Find([]string{"reset"})
	if err != nil {
		t.Errorf("Expected to find reset subcommand, got error: %v", err)
	}
	
	if resetCmd.Use != "reset" {
		t.Errorf("Expected reset subcommand Use to be 'reset', got %s", resetCmd.Use)
	}
}

func TestNewCmdResetCache(t *testing.T) {
	cmd := NewCmdResetCache()
	
	// Test command structure
	if cmd.Use != "reset" {
		t.Errorf("Expected Use to be 'reset', got %s", cmd.Use)
	}
	
	if cmd.Short != "Reset the cache for a project" {
		t.Errorf("Expected Short description, got %s", cmd.Short)
	}
	
	// Test flags exist
	projectFlag := cmd.Flags().Lookup("project")
	if projectFlag == nil {
		t.Error("Expected 'project' flag to exist")
	}
	
	tokenFlag := cmd.Flags().Lookup("token")
	if tokenFlag == nil {
		t.Error("Expected 'token' flag to exist")
	}
	
	// Test RunE is set
	if cmd.RunE == nil {
		t.Error("Expected RunE to be set")
	}
}