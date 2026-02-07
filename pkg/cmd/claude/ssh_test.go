package claude

import (
	"testing"

	"github.com/depot/cli/pkg/ssh"
)

func TestBuildSSHArgs(t *testing.T) {
	tests := []struct {
		name     string
		conn     *ssh.SSHConnectionInfo
		keyFile  string
		command  []string
		wantArgs []string
	}{
		{
			name: "interactive session (no command)",
			conn: &ssh.SSHConnectionInfo{
				Host:     "sandbox-123.depot.dev",
				Port:     2222,
				Username: "depot",
			},
			keyFile: "/tmp/depot-ssh-key-123",
			command: nil,
			wantArgs: []string{
				"-i", "/tmp/depot-ssh-key-123",
				"-p", "2222",
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ServerAliveInterval=30",
				"depot@sandbox-123.depot.dev",
			},
		},
		{
			name: "with command",
			conn: &ssh.SSHConnectionInfo{
				Host:     "sandbox-456.depot.dev",
				Port:     22,
				Username: "root",
			},
			keyFile: "/tmp/depot-ssh-key-456",
			command: []string{"ls", "-la"},
			wantArgs: []string{
				"-i", "/tmp/depot-ssh-key-456",
				"-p", "22",
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ServerAliveInterval=30",
				"root@sandbox-456.depot.dev",
				"ls -la",
			},
		},
		{
			name: "single word command",
			conn: &ssh.SSHConnectionInfo{
				Host:     "host.example.com",
				Port:     2222,
				Username: "depot",
			},
			keyFile: "/tmp/key",
			command: []string{"whoami"},
			wantArgs: []string{
				"-i", "/tmp/key",
				"-p", "2222",
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ServerAliveInterval=30",
				"depot@host.example.com",
				"whoami",
			},
		},
		{
			name: "empty command slice",
			conn: &ssh.SSHConnectionInfo{
				Host:     "host.example.com",
				Port:     2222,
				Username: "depot",
			},
			keyFile: "/tmp/key",
			command: []string{},
			wantArgs: []string{
				"-i", "/tmp/key",
				"-p", "2222",
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ServerAliveInterval=30",
				"depot@host.example.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArgs := ssh.BuildSSHArgs(tt.conn, tt.keyFile, tt.command)

			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("BuildSSHArgs() returned %d args, want %d args\ngot:  %v\nwant: %v", len(gotArgs), len(tt.wantArgs), gotArgs, tt.wantArgs)
				return
			}

			for i, arg := range gotArgs {
				if arg != tt.wantArgs[i] {
					t.Errorf("BuildSSHArgs() args[%d] = %q, want %q", i, arg, tt.wantArgs[i])
				}
			}
		})
	}
}

func TestSSHConnectionInfo(t *testing.T) {
	conn := &ssh.SSHConnectionInfo{
		Host:       "sandbox.depot.dev",
		Port:       2222,
		Username:   "depot",
		PrivateKey: "dGVzdC1rZXk=", // base64("test-key")
	}

	if conn.Host != "sandbox.depot.dev" {
		t.Errorf("Host = %q, want %q", conn.Host, "sandbox.depot.dev")
	}
	if conn.Port != 2222 {
		t.Errorf("Port = %d, want %d", conn.Port, 2222)
	}
	if conn.Username != "depot" {
		t.Errorf("Username = %q, want %q", conn.Username, "depot")
	}
	if conn.PrivateKey != "dGVzdC1rZXk=" {
		t.Errorf("PrivateKey = %q, want %q", conn.PrivateKey, "dGVzdC1rZXk=")
	}
}
