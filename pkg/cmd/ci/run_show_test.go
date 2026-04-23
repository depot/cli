package ci

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestRunShowFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing run-id",
			args:    []string{},
			wantErr: "accepts 1 arg",
		},
		{
			name:    "reaches auth resolution after validation",
			args:    []string{"run-123"},
			wantErr: "missing API token",
		},
		{
			name:    "supports json output flag",
			args:    []string{"run-123", "--output=json"},
			wantErr: "missing API token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DEPOT_TOKEN", "")
			viper.Set("api_token", "")
			cmd := NewCmdRunShow()
			cmd.SetArgs(tt.args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SilenceUsage = true
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestRunCommandRegistersShowAndGetAlias(t *testing.T) {
	cmd := NewCmdRun()

	show, _, err := cmd.Find([]string{"show"})
	if err != nil {
		t.Fatalf("find show: %v", err)
	}
	if show == nil || show.Name() != "show" {
		t.Fatalf("show command not registered")
	}

	get, _, err := cmd.Find([]string{"get"})
	if err != nil {
		t.Fatalf("find get alias: %v", err)
	}
	if get == nil || get.Name() != "show" {
		t.Fatalf("get alias resolved to %q, want show", get.Name())
	}
}
