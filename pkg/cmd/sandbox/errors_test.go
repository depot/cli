package sandbox

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/pty"
)

func TestSandboxExecErrorExplainsAnUnreachableSandbox(t *testing.T) {
	err := sandboxExecError(connect.NewError(connect.CodeNotFound, errors.New("sandbox not found")), "abc123")
	for _, want := range []string{`"abc123"`, "still running", "--session-id", "organization owner"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestSandboxExecErrorKeepsOtherErrors(t *testing.T) {
	cause := connect.NewError(connect.CodeInternal, errors.New("boom"))
	if err := sandboxExecError(cause, "abc123"); !errors.Is(err, cause) {
		t.Fatalf("expected the original error, got %v", err)
	}
}

func TestSandboxPtyErrorExplainsAnUnreachableSandbox(t *testing.T) {
	err := sandboxPtyError(fmt.Errorf("open: %w", pty.ErrSandboxNotFound), "abc123")
	for _, want := range []string{`"abc123"`, "--session-id", "organization owner"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	cause := errors.New("boom")
	if err := sandboxPtyError(cause, "abc123"); !errors.Is(err, cause) {
		t.Fatalf("expected the original error, got %v", err)
	}
	if err := sandboxPtyError(nil, "abc123"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
