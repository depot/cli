//go:build !windows

package pty

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
	"golang.org/x/term"
)

// watchTerminalResize listens for SIGWINCH and sends civ1 resize messages
// via sendCh (legacy CI bastion PTY path).
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

// watchTerminalResizeSandboxV0 is the M34 counterpart that sends
// depot.sandbox.v1.OpenPtyRequest.Resize messages. Used by RunSandboxV0.
func watchTerminalResizeSandboxV0(
	ctx context.Context,
	fd int,
	sendCh chan<- *sandboxv1.OpenPtyRequest,
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
				case sendCh <- &sandboxv1.OpenPtyRequest{
					Input: &sandboxv1.OpenPtyRequest_Resize_{
						Resize: &sandboxv1.OpenPtyRequest_Resize{
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
