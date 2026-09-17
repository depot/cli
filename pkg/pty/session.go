package pty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/depot/cli/pkg/api"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"golang.org/x/term"
)

// SessionOptions configures an interactive PTY session.
//
// Two wires share this struct:
//   - pty.Run (legacy): civ1.DepotComputeService.OpenPtySession — the CI
//     bastion path used by `depot run --ssh` / `depot ssh`. Carries
//     SessionID + SandboxID.
type SessionOptions struct {
	Token     string
	OrgID     string // sent as x-depot-org header for multi-org users
	SandboxID string
	SessionID string
	Cwd       string
	Env       map[string]string
}

// Run opens an interactive PTY session against the legacy CI bastion wire
// (civ1.DepotComputeService.OpenPtySession). Consumed by `depot run --ssh`
// and `depot ssh`.
func Run(ctx context.Context, opts SessionOptions) error {
	fd := int(os.Stdin.Fd())
	rows, cols := 24, 80 //nolint:mnd
	if w, h, err := term.GetSize(fd); err == nil {
		cols, rows = w, h
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("make raw: %w", err)
	}
	defer term.Restore(fd, oldState) //nolint:errcheck

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		term.Restore(fd, oldState) //nolint:errcheck
		os.Exit(1)
	}()
	defer signal.Stop(sigCh)

	client := api.NewComputeClient()
	stream := client.OpenPtySession(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+opts.Token)
	if opts.OrgID != "" {
		stream.RequestHeader().Set("x-depot-org", opts.OrgID)
	}

	if err := stream.Send(&civ1.OpenPtySessionRequest{
		Message: &civ1.OpenPtySessionRequest_Init{
			Init: &civ1.PtySession{
				SandboxId: opts.SandboxID,
				SessionId: opts.SessionID,
				Cwd:       opts.Cwd,
				Env:       opts.Env,
				Rows:      uint32(rows),
				Cols:      uint32(cols),
			},
		},
	}); err != nil {
		return ptySessionSendInitError(err, opts.SandboxID)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sendCh := make(chan *civ1.OpenPtySessionRequest, 1)

	go func() {
		for {
			select {
			case msg := <-sendCh:
				_ = stream.Send(msg)
			case <-ctx.Done():
				_ = stream.CloseRequest()
				return
			}
		}
	}()

	stopResize := watchTerminalResize(ctx, fd, sendCh)
	defer stopResize()

	go func() {
		buf := make([]byte, 4096) //nolint:mnd
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case sendCh <- &civ1.OpenPtySessionRequest{
					Message: &civ1.OpenPtySessionRequest_Stdin{Stdin: data},
				}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		resp, err := stream.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return ptySessionReceiveError(err, opts.SandboxID)
		}
		switch m := resp.GetMessage().(type) {
		case *civ1.OpenPtySessionResponse_Stdout:
			_, _ = os.Stdout.Write(m.Stdout)
		case *civ1.OpenPtySessionResponse_ExitCode:
			fmt.Fprintf(os.Stderr, "\r\n[exit %d]\r\n", m.ExitCode)
			if m.ExitCode != 0 {
				term.Restore(fd, oldState) //nolint:errcheck
				os.Exit(int(m.ExitCode))
			}
			return nil
		}
	}
}
