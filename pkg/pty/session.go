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
type SessionOptions struct {
	Token     string
	SandboxID string
	SessionID string
	Cwd       string
	Env       map[string]string
}

// Run opens an interactive PTY session to the given sandbox.
// It puts the terminal in raw mode, forwards stdin/stdout, handles resize,
// and blocks until the session exits or ctx is cancelled.
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

	// Forward stdin to the stream.
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

	// Read stdout and exit code from the stream.
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
			os.Stdout.Write(m.Stdout) //nolint:errcheck
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
