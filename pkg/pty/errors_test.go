package pty

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestPtySessionErrorMarksAMissingSandbox(t *testing.T) {
	notFound := connect.NewError(connect.CodeNotFound, errors.New("sandbox not found"))
	for name, err := range map[string]error{
		"open":    ptySessionError(notFound, "abc123"),
		"init":    ptySessionSendInitError(notFound, "abc123"),
		"receive": ptySessionReceiveError(notFound, "abc123"),
	} {
		if !errors.Is(err, ErrSandboxNotFound) {
			t.Errorf("%s: %v is not ErrSandboxNotFound", name, err)
		}
		if !strings.Contains(err.Error(), `"abc123"`) {
			t.Errorf("%s: error %q does not name the sandbox", name, err)
		}
	}

	if err := ptySessionReceiveError(connect.NewError(connect.CodeInternal, errors.New("boom")), "abc123"); errors.Is(err, ErrSandboxNotFound) {
		t.Errorf("an internal error was reported as a missing sandbox: %v", err)
	}
}
