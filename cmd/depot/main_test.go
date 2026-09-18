package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/docker/cli/cli"
	"github.com/spf13/cobra"
)

func TestRunMainStandalonePreservesStatusErrorExitCode(t *testing.T) {
	t.Setenv("DEPOT_ERROR_TELEMETRY", "0")
	t.Setenv("DEPOT_NO_UPDATE_NOTIFIER", "1")

	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	os.Args = []string{"depot"}

	originalNewRootCmd := newRootCmd
	t.Cleanup(func() { newRootCmd = originalNewRootCmd })
	newRootCmd = func(string, string) *cobra.Command {
		return &cobra.Command{
			Use: "depot",
			RunE: func(*cobra.Command, []string) error {
				return cli.StatusError{StatusCode: 7}
			},
		}
	}

	if code := runMain(); code != 7 {
		t.Fatalf("expected status code 7, got %d", code)
	}
}

func TestRunMainStandaloneStatusErrorNeverReturnsZero(t *testing.T) {
	t.Setenv("DEPOT_ERROR_TELEMETRY", "0")
	t.Setenv("DEPOT_NO_UPDATE_NOTIFIER", "1")

	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	os.Args = []string{"depot"}

	originalNewRootCmd := newRootCmd
	t.Cleanup(func() { newRootCmd = originalNewRootCmd })
	newRootCmd = func(string, string) *cobra.Command {
		return &cobra.Command{
			Use: "depot",
			RunE: func(*cobra.Command, []string) error {
				return cli.StatusError{StatusCode: 0}
			},
		}
	}

	if code := runMain(); code != 1 {
		t.Fatalf("expected zero status code to normalize to 1, got %d", code)
	}
}

func TestRunMainStandalonePrintsStatusErrorStatus(t *testing.T) {
	t.Setenv("DEPOT_ERROR_TELEMETRY", "0")
	t.Setenv("DEPOT_NO_UPDATE_NOTIFIER", "1")

	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	os.Args = []string{"depot"}

	stderr := captureStderr(t)

	originalNewRootCmd := newRootCmd
	t.Cleanup(func() { newRootCmd = originalNewRootCmd })
	newRootCmd = func(string, string) *cobra.Command {
		return &cobra.Command{
			Use: "depot",
			RunE: func(*cobra.Command, []string) error {
				return cli.StatusError{Status: "exit status 7", StatusCode: 7}
			},
		}
	}

	if code := runMain(); code != 7 {
		t.Fatalf("expected status code 7, got %d", code)
	}
	if got := stderr(); !strings.Contains(got, "exit status 7") {
		t.Fatalf("expected status on stderr, got %q", got)
	}
}

func captureStderr(t *testing.T) func() string {
	t.Helper()
	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	t.Cleanup(func() {
		os.Stderr = original
		_ = reader.Close()
	})

	return func() string {
		_ = writer.Close()
		contents, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		return string(contents)
	}
}
