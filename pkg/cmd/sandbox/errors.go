package sandbox

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/pty"
)

func sandboxUnreachableError(sandboxID string) error {
	return fmt.Errorf(
		"sandbox %q was not found or cannot be reached: check the sandbox ID and --session-id, and that the sandbox "+
			"is still running; an agent sandbox takes no --session-id and can only be reached by an organization owner",
		sandboxID,
	)
}

func sandboxExecError(err error, sandboxID string) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return sandboxUnreachableError(sandboxID)
	}

	return err
}

func sandboxPtyError(err error, sandboxID string) error {
	if errors.Is(err, pty.ErrSandboxNotFound) {
		return sandboxUnreachableError(sandboxID)
	}

	return err
}

func sandboxExecSendInitError(err error, sandboxID string) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return sandboxExecError(err, sandboxID)
	}

	return fmt.Errorf("send init: %w", err)
}

func sandboxExecStreamError(err error, sandboxID string) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return sandboxExecError(err, sandboxID)
	}

	return fmt.Errorf("stream error: %w", err)
}
