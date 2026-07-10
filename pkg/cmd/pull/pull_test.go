package pull

import (
	"strings"
	"testing"
)

func TestPullArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "missing"},
		{name: "empty", args: []string{""}, wantErr: "build ID or tag must not be empty"},
		{name: "build ID", args: []string{"build-id"}},
		{name: "whitespace", args: []string{" "}},
		{name: "too many", args: []string{"build-id", "extra"}, wantErr: "requires at most 1 argument"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCmdPull()
			err := cmd.Args(cmd, tt.args)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
