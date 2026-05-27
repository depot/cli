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
	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
	"golang.org/x/term"
)

// RunSandboxV0 opens an interactive PTY session against the depot.sandbox.v1
// SandboxService.OpenPty bidi-streaming RPC. M34 (D-M34-L) consumer of the
// v0 sandbox surface; called by `depot sandbox shell`. Session-id is gone —
// the wire takes a SandboxRef only.
//
// Wire shape (pty.proto):
//   - First request MUST carry Start{sandbox, env, rows, cols, cwd}
//   - Subsequent requests carry Stdin bytes or Resize{rows, cols}
//   - Response stream emits Data bytes terminated by a single Exit{exit_code}
func RunSandboxV0(ctx context.Context, opts SessionOptions) error {
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

	client := api.NewSandboxV0Client()
	stream := client.OpenPty(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+opts.Token)
	if opts.OrgID != "" {
		stream.RequestHeader().Set("x-depot-org", opts.OrgID)
	}

	startRows := uint32(rows)
	startCols := uint32(cols)
	start := &sandboxv1.OpenPtyRequest_Start{
		Sandbox: &sandboxv1.SandboxRef{Selector: &sandboxv1.SandboxRef_Id{Id: opts.SandboxID}},
		Env:     opts.Env,
		Rows:    &startRows,
		Cols:    &startCols,
	}
	if opts.Cwd != "" {
		start.Cwd = &opts.Cwd
	}
	if err := stream.Send(&sandboxv1.OpenPtyRequest{
		Input: &sandboxv1.OpenPtyRequest_Start_{Start: start},
	}); err != nil {
		return fmt.Errorf("send pty start: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sendCh := make(chan *sandboxv1.OpenPtyRequest, 1)

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

	stopResize := watchTerminalResizeSandboxV0(ctx, fd, sendCh)
	defer stopResize()

	if len(opts.StdinPrefix) > 0 {
		if err := stream.Send(&sandboxv1.OpenPtyRequest{
			Input: &sandboxv1.OpenPtyRequest_Stdin{Stdin: opts.StdinPrefix},
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
				case sendCh <- &sandboxv1.OpenPtyRequest{
					Input: &sandboxv1.OpenPtyRequest_Stdin{Stdin: data},
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
		switch m := resp.Output.(type) {
		case *sandboxv1.OpenPtyResponse_Data:
			_, _ = os.Stdout.Write(m.Data)
		case *sandboxv1.OpenPtyResponse_Exit_:
			if m.Exit != nil {
				fmt.Fprintf(os.Stderr, "\r\n[exit %d]\r\n", m.Exit.ExitCode)
				if m.Exit.ExitCode != 0 {
					term.Restore(fd, oldState) //nolint:errcheck
					os.Exit(int(m.Exit.ExitCode))
				}
			}
			return nil
		}
	}
}
