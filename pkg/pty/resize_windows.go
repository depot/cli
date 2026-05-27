package pty

import (
	"context"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
)

// watchTerminalResize is a no-op on Windows since SIGWINCH is not available.
func watchTerminalResize(
	_ context.Context,
	_ int,
	_ chan<- *civ1.OpenPtySessionRequest,
) func() {
	return func() {}
}

// watchTerminalResizeSandboxV0 — Windows no-op for the sandboxv1 PTY path.
func watchTerminalResizeSandboxV0(
	_ context.Context,
	_ int,
	_ chan<- *sandboxv1.OpenPtyRequest,
) func() {
	return func() {}
}
