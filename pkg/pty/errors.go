package pty

import (
	"fmt"

	"connectrpc.com/connect"
)

func ptySessionError(err error, sandboxID string) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return fmt.Errorf("sandbox %q was not found; check that the sandbox ID is correct and the sandbox is still running", sandboxID)
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
