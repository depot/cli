package ci

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseDispatchInputs(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    map[string]string
		wantErr string
	}{
		{name: "nil", in: nil, want: nil},
		{name: "empty slice", in: []string{}, want: nil},
		{
			name: "single pair",
			in:   []string{"env=staging"},
			want: map[string]string{"env": "staging"},
		},
		{
			name: "multiple pairs",
			in:   []string{"env=staging", "debug=true"},
			want: map[string]string{"env": "staging", "debug": "true"},
		},
		{
			name: "value containing equals is preserved",
			in:   []string{"jwt=a=b=c"},
			want: map[string]string{"jwt": "a=b=c"},
		},
		{
			name:    "missing equals sign",
			in:      []string{"envstaging"},
			wantErr: "expected key=value",
		},
		{
			name:    "empty key",
			in:      []string{"=value"},
			wantErr: "expected key=value",
		},
		{
			name:    "duplicate key",
			in:      []string{"env=a", "env=b"},
			wantErr: "duplicate",
		},
		{
			name: "empty value is allowed",
			in:   []string{"env="},
			want: map[string]string{"env": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDispatchInputs(tt.in)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("expected %d entries, got %d (%v)", len(tt.want), len(got), got)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q: expected %q, got %q", k, v, got[k])
				}
			}
		})
	}
}

func TestDispatchRequiredFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing repo",
			args:    []string{"--workflow=.depot/workflows/deploy.yml", "--ref=main"},
			wantErr: `"repo" not set`,
		},
		{
			name:    "missing workflow",
			args:    []string{"--repo=depot/cli", "--ref=main"},
			wantErr: `"workflow" not set`,
		},
		{
			name:    "missing ref",
			args:    []string{"--repo=depot/cli", "--workflow=.depot/workflows/deploy.yml"},
			wantErr: `"ref" not set`,
		},
		{
			name:    "invalid input",
			args:    []string{"--repo=depot/cli", "--workflow=.depot/workflows/deploy.yml", "--ref=main", "--input=notvalid"},
			wantErr: "expected key=value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCmdDispatch()
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
