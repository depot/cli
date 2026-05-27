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
//   - pty.RunSandboxV0 (M34): depot.sandbox.v1.SandboxService.OpenPty —
//     the new sandboxv1 wire used by `depot sandbox shell`. Session-id is
//     gone (D-M34-M); RunSandboxV0 ignores it.
//
// Once the CI bastion verbs migrate to sandboxv1 (Theme 3 follow-on), the
// legacy Run can be retired and SessionID dropped from this struct.
type SessionOptions struct {
	Token     string
	OrgID     string // sent as x-depot-org header for multi-org users
	SandboxID string
	SessionID string // legacy CI bastion only; ignored by RunSandboxV0 (D-M34-M)
	Cwd       string
	Env       map[string]string

	// StdinPrefix is sent as the first stdin chunk after the pty session is
	// open, before forwarding os.Stdin. Used by `depot sandbox shell` to
	// inject on.shell entries into the login shell. Must be terminated with
	// "\n" so the shell's line discipline flushes it.
	StdinPrefix []byte
}

// Run opens an interactive PTY session against the legacy CI bastion wire
// (civ1.DepotComputeService.OpenPtySession). Consumed by `depot run --ssh`
// and `depot ssh`. Sandbox v0 callers (`depot sandbox shell`) use
// RunSandboxV0 instead.
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
		return fmt.Errorf("send pty init: %w", err)
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

	if len(opts.StdinPrefix) > 0 {
		if err := stream.Send(&civ1.OpenPtySessionRequest{
			Message: &civ1.OpenPtySessionRequest_Stdin{Stdin: opts.StdinPrefix},
		}); err != nil {
			return fmt.Errorf("send on.shell prefix: %w", err)
		}
	}

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
			return fmt.Errorf("recv: %w", err)
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
