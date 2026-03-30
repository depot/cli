//go:build !windows

package pty

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"golang.org/x/term"
)

// watchTerminalResize listens for SIGWINCH and sends resize messages via sendCh.
// Stops when ctx is cancelled. Returns a cleanup function to stop watching.
func watchTerminalResize(
	ctx context.Context,
	fd int,
	sendCh chan<- *civ1.OpenPtySessionRequest,
) func() {
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-sigwinch:
				if !ok {
					return
				}
				w, h, err := term.GetSize(fd)
				if err != nil {
					continue
				}
				select {
				case sendCh <- &civ1.OpenPtySessionRequest{
					Message: &civ1.OpenPtySessionRequest_WindowResize{
						WindowResize: &civ1.WindowResize{
							Rows: uint32(h),
							Cols: uint32(w),
						},
					},
				}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return func() { signal.Stop(sigwinch) }
}
