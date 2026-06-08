package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
)

// Every sandbox id passed over the wire flows through sandboxRef(). This pins
// the wire shape so a future refactor cannot silently regress to a bare string
// id without the oneof envelope.
func TestSandboxRef_PinnedShape(t *testing.T) {
	r := sandboxRef("cs-abc123")
	if r == nil {
		t.Fatal("sandboxRef returned nil")
	}
	sel, ok := r.Selector.(*sandboxv1.SandboxRef_Id)
	if !ok {
		t.Fatalf("expected SandboxRef_Id selector, got %T", r.Selector)
	}
	if sel.Id != "cs-abc123" {
		t.Errorf("id = %q, want cs-abc123", sel.Id)
	}
}

func TestConsumeCommandEventStreamReturnsWriterErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		events    []*sandboxv1.SandboxCommandExecutionEvent
		stdout    io.Writer
		stderr    io.Writer
		wantError string
	}{
		{
			name: "stdout",
			events: []*sandboxv1.SandboxCommandExecutionEvent{{
				Event: &sandboxv1.SandboxCommandExecutionEvent_Stdout{
					Stdout: &sandboxv1.SandboxCommandExecutionEvent_StdoutBytes{Data: []byte("out")},
				},
			}},
			stdout:    errWriter{},
			stderr:    io.Discard,
			wantError: "write stdout",
		},
		{
			name: "stderr",
			events: []*sandboxv1.SandboxCommandExecutionEvent{{
				Event: &sandboxv1.SandboxCommandExecutionEvent_Stderr{
					Stderr: &sandboxv1.SandboxCommandExecutionEvent_StderrBytes{Data: []byte("err")},
				},
			}},
			stdout:    io.Discard,
			stderr:    errWriter{},
			wantError: "write stderr",
		},
		{
			name: "command error",
			events: []*sandboxv1.SandboxCommandExecutionEvent{{
				Event: &sandboxv1.SandboxCommandExecutionEvent_Error_{
					Error: &sandboxv1.SandboxCommandExecutionEvent_Error{Reason: "degraded"},
				},
			}},
			stdout:    io.Discard,
			stderr:    errWriter{},
			wantError: "write stderr",
		},
		{
			name: "evicted",
			events: []*sandboxv1.SandboxCommandExecutionEvent{{
				Event: &sandboxv1.SandboxCommandExecutionEvent_Evicted{
					Evicted: &sandboxv1.SandboxCommandExecutionEvent_EvictedEarlyData{
						DroppedBytesStdout: 1,
						DroppedBytesStderr: 2,
					},
				},
			}},
			stdout:    io.Discard,
			stderr:    errWriter{},
			wantError: "write stderr",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := sandboxCommandEventStream(t, tc.events)
			_, err := consumeCommandEventStream(stream, tc.stdout, tc.stderr)
			if err == nil {
				t.Fatal("expected writer error")
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %q, want %q", err, tc.wantError)
			}
		})
	}
}

func TestConsumeCommandEventStreamSuccess(t *testing.T) {
	stream := sandboxCommandEventStream(t, []*sandboxv1.SandboxCommandExecutionEvent{
		{
			Event: &sandboxv1.SandboxCommandExecutionEvent_Stdout{
				Stdout: &sandboxv1.SandboxCommandExecutionEvent_StdoutBytes{Data: []byte("out")},
			},
		},
		{
			Event: &sandboxv1.SandboxCommandExecutionEvent_Finished_{
				Finished: &sandboxv1.SandboxCommandExecutionEvent_Finished{ExitCode: 7},
			},
		},
	})
	var stdout bytes.Buffer
	exit, err := consumeCommandEventStream(stream, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("consumeCommandEventStream returned error: %v", err)
	}
	if exit != 7 {
		t.Fatalf("exit = %d, want 7", exit)
	}
	if stdout.String() != "out" {
		t.Fatalf("stdout = %q, want out", stdout.String())
	}
}

func TestConsumeCommandEventStreamReturnsCommandErrorFrame(t *testing.T) {
	stream := sandboxCommandEventStream(t, []*sandboxv1.SandboxCommandExecutionEvent{
		{
			Event: &sandboxv1.SandboxCommandExecutionEvent_Error_{
				Error: &sandboxv1.SandboxCommandExecutionEvent_Error{Reason: "degraded"},
			},
		},
		{
			Event: &sandboxv1.SandboxCommandExecutionEvent_Finished_{
				Finished: &sandboxv1.SandboxCommandExecutionEvent_Finished{ExitCode: 0},
			},
		},
	})
	var stderr bytes.Buffer
	_, err := consumeCommandEventStream(stream, io.Discard, &stderr)
	if err == nil {
		t.Fatal("expected command error frame to fail")
	}
	if !strings.Contains(err.Error(), "command error: degraded") {
		t.Fatalf("error = %q, want command error", err)
	}
	if !strings.Contains(stderr.String(), "[command-error] degraded") {
		t.Fatalf("stderr = %q, want command diagnostic", stderr.String())
	}
}

func sandboxCommandEventStream(t *testing.T, events []*sandboxv1.SandboxCommandExecutionEvent) *connect.ServerStreamForClient[sandboxv1.SandboxCommandExecutionEvent] {
	t.Helper()
	const procedure = "/test.Sandbox/Stream"
	handler := connect.NewServerStreamHandler[sandboxv1.RunCommandRequest, sandboxv1.SandboxCommandExecutionEvent](
		procedure,
		func(_ context.Context, _ *connect.Request[sandboxv1.RunCommandRequest], stream *connect.ServerStream[sandboxv1.SandboxCommandExecutionEvent]) error {
			for _, event := range events {
				if err := stream.Send(event); err != nil {
					return err
				}
			}
			return nil
		},
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	client := connect.NewClient[sandboxv1.RunCommandRequest, sandboxv1.SandboxCommandExecutionEvent](server.Client(), server.URL+procedure)
	stream, err := client.CallServerStream(context.Background(), connect.NewRequest(&sandboxv1.RunCommandRequest{}))
	if err != nil {
		t.Fatalf("CallServerStream: %v", err)
	}
	return stream
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errors.New("writer failed")
}
