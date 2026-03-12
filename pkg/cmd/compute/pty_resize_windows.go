package compute

import (
	"context"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

// watchTerminalResize is a no-op on Windows since SIGWINCH is not available.
func watchTerminalResize(
	_ context.Context,
	_ int,
	_ chan<- *civ1.OpenPtySessionRequest,
) func() {
	return func() {}
}
