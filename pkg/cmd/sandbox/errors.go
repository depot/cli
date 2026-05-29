package sandbox

import (
	"fmt"

	"connectrpc.com/connect"
)

func sandboxExecError(err error, sandboxID string) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return fmt.Errorf("sandbox %q was not found; check that the sandbox ID is correct and the sandbox is still running", sandboxID)
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
