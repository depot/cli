package pty

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
)

// ErrSandboxNotFound marks a session error caused by the API not finding the sandbox for the caller.
var ErrSandboxNotFound = errors.New("sandbox not found")

type sandboxNotFoundError struct{ sandboxID string }

func (e sandboxNotFoundError) Error() string {
	return fmt.Sprintf("sandbox %q was not found; check that the sandbox ID is correct and the sandbox is still running", e.sandboxID)
}

func (sandboxNotFoundError) Is(target error) bool { return target == ErrSandboxNotFound }

func ptySessionError(err error, sandboxID string) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return sandboxNotFoundError{sandboxID: sandboxID}
	}

	return err
}

func ptySessionSendInitError(err error, sandboxID string) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return ptySessionError(err, sandboxID)
	}

	return fmt.Errorf("send pty init: %w", err)
}

func ptySessionReceiveError(err error, sandboxID string) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return ptySessionError(err, sandboxID)
	}

	return fmt.Errorf("recv: %w", err)
}
