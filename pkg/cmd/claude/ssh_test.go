package claude

import (
	"testing"

	"github.com/depot/cli/pkg/ssh"
)

func TestParseTmateSSHURL(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantArgs  []string
		wantErr   bool
		errSubstr string
	}{
		{
			name:  "valid tmate URL",
			input: "ssh abc123@nyc1.tmate.io",
			wantArgs: []string{
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ServerAliveInterval=30",
				"abc123@nyc1.tmate.io",
			},
			wantErr: false,
		},
		{
			name:  "valid tmate URL with long session token",
			input: "ssh xyzABC123def456@lon1.tmate.io",
			wantArgs: []string{
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ServerAliveInterval=30",
				"xyzABC123def456@lon1.tmate.io",
			},
			wantErr: false,
		},
		{
			name:  "valid tmate URL with port",
			input: "ssh session@tmate.example.com",
			wantArgs: []string{
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ServerAliveInterval=30",
				"session@tmate.example.com",
			},
			wantErr: false,
		},
		{
			name:      "empty string",
			input:     "",
			wantArgs:  nil,
			wantErr:   true,
			errSubstr: "invalid tmate SSH URL format",
		},
		{
			name:      "only ssh command",
			input:     "ssh",
			wantArgs:  nil,
			wantErr:   true,
			errSubstr: "invalid tmate SSH URL format",
		},
		{
			name:      "no ssh prefix",
			input:     "abc123@nyc1.tmate.io",
			wantArgs:  nil,
			wantErr:   true,
			errSubstr: "invalid tmate SSH URL format",
		},
		{
			name:  "URL with extra whitespace",
			input: "ssh   abc123@nyc1.tmate.io",
			wantArgs: []string{
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ServerAliveInterval=30",
				"abc123@nyc1.tmate.io",
			},
			wantErr: false,
		},
		{
			name:  "URL with trailing content (ignored)",
			input: "ssh abc123@nyc1.tmate.io extra",
			wantArgs: []string{
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ServerAliveInterval=30",
				"abc123@nyc1.tmate.io",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArgs, err := ssh.ParseTmateSSHURL(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ssh.ParseTmateSSHURL(%q) expected error, got nil", tt.input)
					return
				}
				if tt.errSubstr != "" && !contains(err.Error(), tt.errSubstr) {
					t.Errorf("ssh.ParseTmateSSHURL(%q) error = %q, want error containing %q", tt.input, err.Error(), tt.errSubstr)
				}
				return
			}

			if err != nil {
				t.Errorf("ssh.ParseTmateSSHURL(%q) unexpected error: %v", tt.input, err)
				return
			}

			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("ssh.ParseTmateSSHURL(%q) returned %d args, want %d args", tt.input, len(gotArgs), len(tt.wantArgs))
				return
			}

			for i, arg := range gotArgs {
				if arg != tt.wantArgs[i] {
					t.Errorf("ssh.ParseTmateSSHURL(%q) args[%d] = %q, want %q", tt.input, i, arg, tt.wantArgs[i])
				}
			}
		})
	}
}

// contains checks if substr is contained in s
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && searchSubstring(s, substr)))
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
