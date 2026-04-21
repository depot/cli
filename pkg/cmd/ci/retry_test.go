package ci

import (
	"bytes"
	"strings"
	"testing"
)

func TestRetryFlagValidation(t *testing.T) {
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
			name:    "no mode flag",
			args:    []string{"run-123"},
			wantErr: "one of --job or --failed",
		},
		{
			name:    "both mode flags",
			args:    []string{"run-123", "--job=job-1", "--failed"},
			wantErr: "mutually exclusive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCmdRetry()
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
