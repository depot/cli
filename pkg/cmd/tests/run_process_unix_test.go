//go:build !windows

package tests

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunShellCommandCancellationKillsChildProcesses(t *testing.T) {
	dir := t.TempDir()
	readyPath := filepath.Join(dir, "ready")
	survivedPath := filepath.Join(dir, "survived")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		_, err := runShellCommand(
			ctx,
			"(trap '' TERM; touch "+shellQuote(readyPath)+"; sleep 10; touch "+shellQuote(survivedPath)+") & wait",
			nil,
			&stdout,
			&stderr,
		)
		done <- err
	}()

	waitForFile(t, readyPath)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expected canceled command to exit")
	}

	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(survivedPath); !os.IsNotExist(err) {
		t.Fatalf("expected child process to be killed before writing survived marker, stat err %v", err)
	}
}

func TestRunShellCommandCancellationAllowsTermCleanup(t *testing.T) {
	dir := t.TempDir()
	readyPath := filepath.Join(dir, "ready")
	termPath := filepath.Join(dir, "term")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		_, err := runShellCommand(
			ctx,
			"trap 'touch "+shellQuote(termPath)+"; exit 0' TERM; touch "+shellQuote(readyPath)+"; sleep 10",
			nil,
			&stdout,
			&stderr,
		)
		done <- err
	}()

	waitForFile(t, readyPath)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected command to exit during TERM grace period")
	}
	if _, err := os.Stat(termPath); err != nil {
		t.Fatalf("expected child cleanup trap to run, stat err %v", err)
	}
}

func TestRunShellCommandCancellationKillsIgnoredTermChildAfterShellExits(t *testing.T) {
	dir := t.TempDir()
	readyPath := filepath.Join(dir, "ready")
	childPIDPath := filepath.Join(dir, "child-pid")
	survivedPath := filepath.Join(dir, "survived")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		_, err := runShellCommand(
			ctx,
			"(trap '' TERM; touch "+shellQuote(readyPath)+"; sleep 10; touch "+shellQuote(survivedPath)+") & child=$!; echo $child > "+shellQuote(childPIDPath)+"; trap 'exit 0' TERM; wait $child",
			nil,
			&stdout,
			&stderr,
		)
		done <- err
	}()

	waitForFile(t, readyPath)
	childPID := readPID(t, childPIDPath)
	pgid, err := syscall.Getpgid(childPID)
	if err != nil {
		t.Fatalf("expected child process group before cancellation: %v", err)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expected canceled command to exit")
	}
	if processGroupExists(pgid) {
		t.Fatalf("expected canceled command process group %d to be gone", pgid)
	}
	if _, err := os.Stat(survivedPath); !os.IsNotExist(err) {
		t.Fatalf("expected ignored-TERM child to be killed before writing survived marker, stat err %v", err)
	}
}

func TestRunShellCommandReturnsSignalExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode, err := runShellCommand(context.Background(), "kill -TERM $$", nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected signaled command error")
	}
	if exitCode != 143 {
		t.Fatalf("expected SIGTERM exit code 143, got %d", exitCode)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil {
		t.Fatalf("expected pid in %s, got %q", path, string(contents))
	}
	return pid
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
