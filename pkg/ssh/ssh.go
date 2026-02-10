package ssh

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// SSHConnectionInfo contains the SSH connection parameters returned by the API.
type SSHConnectionInfo struct {
	Host       string
	Port       int32
	Username   string
	PrivateKey string // base64-encoded PEM private key
}

// convertToOpenSSHKey takes PEM bytes that may be in PKCS#8 format and converts
// them to OpenSSH format that the ssh CLI understands.
func convertToOpenSSHKey(pemBytes []byte) ([]byte, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("failed to parse PEM block")
	}

	// If it's already in OpenSSH format, return as-is
	if block.Type == "OPENSSH PRIVATE KEY" {
		return pemBytes, nil
	}

	// Parse PKCS#8 key
	privKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Not PKCS#8, try returning the original - ssh might handle it
		return pemBytes, nil
	}

	// Marshal to OpenSSH format
	opensshKey, err := gossh.MarshalPrivateKey(privKey, "")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal key to OpenSSH format: %w", err)
	}

	return pem.EncodeToMemory(opensshKey), nil
}

// writePrivateKey writes the private key to a temp file with 0600 permissions and returns
// the file path and a cleanup function. The key may be base64-encoded or a raw PEM string.
func writePrivateKey(key string) (path string, cleanup func(), err error) {
	if strings.TrimSpace(key) == "" {
		return "", nil, fmt.Errorf("private key is empty — the API may not have returned it for this session")
	}

	var keyBytes []byte

	if strings.HasPrefix(strings.TrimSpace(key), "-----") {
		// Already a raw PEM key
		keyBytes = []byte(key)
	} else {
		// Try base64 decoding
		keyBytes, err = base64.StdEncoding.DecodeString(key)
		if err != nil {
			// Try base64 without padding (RawStdEncoding)
			keyBytes, err = base64.RawStdEncoding.DecodeString(key)
			if err != nil {
				return "", nil, fmt.Errorf("failed to decode private key: %w", err)
			}
		}
	}

	// Convert PKCS#8 to OpenSSH format if needed (macOS ssh doesn't support PKCS#8)
	keyBytes, err = convertToOpenSSHKey(keyBytes)
	if err != nil {
		return "", nil, err
	}

	// Ensure the key ends with a newline
	if len(keyBytes) > 0 && keyBytes[len(keyBytes)-1] != '\n' {
		keyBytes = append(keyBytes, '\n')
	}

	f, err := os.CreateTemp("", "depot-ssh-key-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp key file: %w", err)
	}

	if err := os.Chmod(f.Name(), 0600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("failed to set key file permissions: %w", err)
	}

	if _, err := f.Write(keyBytes); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("failed to write key file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("failed to close key file: %w", err)
	}

	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

// BuildSSHArgs constructs the SSH command arguments for connecting to a sandbox.
func BuildSSHArgs(conn *SSHConnectionInfo, keyFile string, command []string) []string {
	args := []string{
		"-i", keyFile,
		"-p", fmt.Sprintf("%d", conn.Port),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ServerAliveInterval=30",
		fmt.Sprintf("%s@%s", conn.Username, conn.Host),
	}

	if len(command) > 0 {
		args = append(args, strings.Join(command, " "))
	}

	return args
}

// ExecSSH connects to a sandbox via SSH interactively.
// It retries the connection up to 10 times with a 2-second delay if sshd isn't ready yet.
func ExecSSH(conn *SSHConnectionInfo) error {
	keyFile, cleanup, err := writePrivateKey(conn.PrivateKey)
	if err != nil {
		return err
	}
	defer cleanup()

	args := BuildSSHArgs(conn, keyFile, nil)

	// Retry loop: sshd may not be ready immediately after the API reports the host/port
	maxRetries := 10
	for attempt := 1; attempt <= maxRetries; attempt++ {
		cmd := exec.Command("ssh", args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout

		// Suppress SSH stderr noise (e.g. "Connection closed by ...") during retries
		if attempt < maxRetries {
			cmd.Stderr = io.Discard
		} else {
			cmd.Stderr = os.Stderr
		}

		err = cmd.Run()
		if err == nil {
			return nil
		}

		// Exit code 255 means SSH connection failed (server not ready, connection refused, etc.)
		// Any other exit code means SSH connected but the remote session ended with that code
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 255 && attempt < maxRetries {
			time.Sleep(2 * time.Second)
			continue
		}

		return err
	}

	return err
}

// ExecSSHCommand executes a command in a sandbox via SSH and returns the exit code.
// It retries the connection up to 10 times with a 2-second delay if sshd isn't ready yet.
func ExecSSHCommand(conn *SSHConnectionInfo, command []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	keyFile, cleanup, err := writePrivateKey(conn.PrivateKey)
	if err != nil {
		return 1, err
	}
	defer cleanup()

	args := BuildSSHArgs(conn, keyFile, command)

	maxRetries := 10
	for attempt := 1; attempt <= maxRetries; attempt++ {
		cmd := exec.Command("ssh", args...)
		cmd.Stdin = stdin
		cmd.Stdout = stdout
		cmd.Stderr = stderr

		err = cmd.Run()
		if err == nil {
			return 0, nil
		}

		if exitErr, ok := err.(*exec.ExitError); ok {
			// Exit code 255 means SSH connection failed - retry if sshd isn't ready
			if exitErr.ExitCode() == 255 && attempt < maxRetries {
				fmt.Fprintf(stderr, "SSH connection failed, retrying in 2s (%d/%d)...\n", attempt, maxRetries)
				time.Sleep(2 * time.Second)
				continue
			}
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}

	// Should not reach here, but handle edge case
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	return 1, err
}

// ConnectionInfo contains information about an SSH connection for display purposes.
type ConnectionInfo struct {
	SandboxID      string
	SessionID      string
	Host           string
	Port           int32
	Username       string
	TimeoutMinutes int
	CommandName    string // e.g., "depot claude" or "depot sandbox"
}

// PrintConnectionInfo outputs connection details to the given writer.
func PrintConnectionInfo(info *ConnectionInfo, stdout io.Writer) {
	fmt.Fprintf(stdout, "\nSandbox ready!\n")
	if info.SandboxID != "" {
		fmt.Fprintf(stdout, "Sandbox ID: %s\n", info.SandboxID)
	}
	fmt.Fprintf(stdout, "Session ID: %s\n", info.SessionID)
	if info.TimeoutMinutes > 0 {
		fmt.Fprintf(stdout, "Timeout: %d minutes\n", info.TimeoutMinutes)
	}
	if info.CommandName != "" {
		fmt.Fprintf(stdout, "\nTo connect:\n  %s connect %s\n", info.CommandName, info.SessionID)
	}
	if info.SandboxID != "" {
		fmt.Fprintf(stdout, "\nTo exec:\n  %s exec %s <command>\n", info.CommandName, info.SandboxID)
	}
}

// PrintConnecting outputs a message indicating that we're connecting.
func PrintConnecting(conn *SSHConnectionInfo, sessionID, commandName string, stdout io.Writer) {
	fmt.Fprintf(stdout, "Tip: To reconnect later, run:\n")
	fmt.Fprintf(stdout, "  %s connect %s\n\n", commandName, sessionID)
}
