package ci

import (
	"bytes"
	"strings"
	"testing"
)

func TestCancelFlagValidation(t *testing.T) {
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
			name:    "no scope flag",
			args:    []string{"run-123"},
			wantErr: "one of --workflow or --job",
		},
		{
			name:    "both scope flags",
			args:    []string{"run-123", "--workflow=wf-1", "--job=job-1"},
			wantErr: "none of the others can be",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCmdCancel()
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
