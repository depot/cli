package ssh

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ParseTmateSSHURL parses a tmate SSH URL and returns the SSH arguments.
// The URL is expected to be in the format "ssh XXX@host" where XXX is a session token.
func ParseTmateSSHURL(tmateSSHURL string) ([]string, error) {
	// Parse "ssh XXX@host" format
	parts := strings.Fields(tmateSSHURL)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid tmate SSH URL format: %s", tmateSSHURL)
	}

	// Extract user@host from the URL
	userHost := parts[1]

	// Build SSH command with appropriate options
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ServerAliveInterval=30",
		userHost,
	}

	return sshArgs, nil
}

// ExecSSH connects to a tmate session via SSH.
// This replaces the current process with SSH.
func ExecSSH(tmateSSHURL string) error {
	sshArgs, err := ParseTmateSSHURL(tmateSSHURL)
	if err != nil {
		return err
	}

	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// ConnectionInfo contains information about an SSH connection
type ConnectionInfo struct {
	SessionID      string
	SSHURL         string
	WebURL         string
	TimeoutMinutes int
	CommandName    string // e.g., "depot claude" or "depot sandbox"
}

// PrintConnectionInfo outputs connection details to the given writer
func PrintConnectionInfo(info *ConnectionInfo, stdout io.Writer) {
	fmt.Fprintf(stdout, "\nSSH sandbox ready!\n")
	fmt.Fprintf(stdout, "Session ID: %s\n", info.SessionID)
	if info.TimeoutMinutes > 0 {
		fmt.Fprintf(stdout, "Timeout: %d minutes\n", info.TimeoutMinutes)
	}
	fmt.Fprintf(stdout, "\n")
	fmt.Fprintf(stdout, "Connect via SSH:\n  %s\n", info.SSHURL)
	if info.WebURL != "" {
		fmt.Fprintf(stdout, "\nOr via web browser:\n  %s\n", info.WebURL)
	}
	if info.CommandName != "" {
		fmt.Fprintf(stdout, "\nTo reconnect later:\n  %s connect %s\n", info.CommandName, info.SessionID)
	}
}

// PrintConnecting outputs a message indicating that we're connecting
func PrintConnecting(sshURL, sessionID, commandName string, stdout io.Writer) {
	fmt.Fprintf(stdout, "Connecting...\n")
	fmt.Fprintf(stdout, "SSH URL: %s\n\n", sshURL)
	fmt.Fprintf(stdout, "Tip: Your session runs in tmate. To reconnect later, run:\n")
	fmt.Fprintf(stdout, "  %s connect %s\n\n", commandName, sessionID)
}
