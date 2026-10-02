package agent

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
)

type fakeAgentService struct {
	agentv1connect.UnimplementedDepotAgentServiceHandler

	mu      sync.Mutex
	watches int
	// streams[i] is sent on the i-th WatchSession call;
	// the last stream is held open until the client goes away.
	streams [][]*agentv1.WatchSessionResponse
	inputs  []*agentv1.SendInputRequest
	auth    []string
	after   []uint64
	// createFailures CreateSession calls fail Unavailable before one succeeds.
	createFailures int
	creates        []*agentv1.CreateSessionRequest
}

func (f *fakeAgentService) WatchSession(ctx context.Context, req *connect.Request[agentv1.WatchSessionRequest], stream *connect.ServerStream[agentv1.WatchSessionResponse]) error {
	f.mu.Lock()
	call := f.watches
	f.watches++
	f.auth = append(f.auth, req.Header().Get("Authorization")+" "+req.Header().Get("x-depot-org"))
	f.after = append(f.after, req.Msg.GetAfterSeq())
	f.mu.Unlock()

	if call >= len(f.streams) {
		<-ctx.Done()
		return nil
	}
	for _, msg := range f.streams[call] {
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
	if call == len(f.streams)-1 {
		<-ctx.Done()
	}
	return nil
}

func (f *fakeAgentService) CreateSession(_ context.Context, req *connect.Request[agentv1.CreateSessionRequest]) (*connect.Response[agentv1.CreateSessionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, req.Msg)
	if len(f.creates) <= f.createFailures {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("try again"))
	}
	return connect.NewResponse(&agentv1.CreateSessionResponse{Session: &agentv1.DepotAgentSession{SessionId: "s1"}}), nil
}

func (f *fakeAgentService) SendInput(_ context.Context, req *connect.Request[agentv1.SendInputRequest]) (*connect.Response[agentv1.SendInputResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, req.Msg)
	return connect.NewResponse(&agentv1.SendInputResponse{Input: &agentv1.DepotAgentInput{InputId: "in_1"}}), nil
}

func (f *fakeAgentService) InterruptSession(_ context.Context, req *connect.Request[agentv1.InterruptSessionRequest]) (*connect.Response[agentv1.InterruptSessionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, &agentv1.SendInputRequest{
		SessionId: req.Msg.SessionId,
		Mode:      ptr("interrupt"),
	})
	return connect.NewResponse(&agentv1.InterruptSessionResponse{Input: &agentv1.DepotAgentInput{InputId: "in_2"}}), nil
}

func startFake(t *testing.T, f *fakeAgentService) *session {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewDepotAgentServiceHandler(f))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &session{
		client: agentv1connect.NewDepotAgentServiceClient(srv.Client(), srv.URL),
		token:  "tok",
		orgID:  "org",
	}
}

func frame(status string, view string) *agentv1.WatchSessionResponse {
	return &agentv1.WatchSessionResponse{Session: &agentv1.DepotAgentSession{SessionId: "s1", Status: status}, ViewJson: view}
}

const (
	idle    = "idle"
	running = "running"
)

func TestWatchUntilSettledWaitsForRunningAndReconnects(t *testing.T) {
	first := frame(idle, "")
	first.ViewSeq = 3
	f := &fakeAgentService{streams: [][]*agentv1.WatchSessionResponse{
		// A fresh session is idle with its first input still pending:
		// that must not count as settled.
		{first},
		// The server ends the first stream cleanly; the client reconnects.
		{frame(running, viewOne), frame(idle, viewTwo)},
	}}
	s := startFake(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := watchSession(ctx, s, "s1", NewRenderer(&out), true); err != nil {
		t.Fatalf("watchSession: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("watchSession returned only because the test timed out; watches=%d out:\n%s", f.watches, out.String())
	}
	if f.watches != 2 {
		t.Fatalf("expected 2 WatchSession calls, got %d", f.watches)
	}
	if f.after[0] != 0 || f.after[1] != 3 {
		t.Fatalf("reconnect should resume after the last view, sent after_seq %v", f.after)
	}
	if f.auth[0] != "Bearer tok org" {
		t.Fatalf("watch sent auth %q", f.auth[0])
	}
	got := out.String()
	if !strings.HasPrefix(got, "[idle]\n") || !strings.Contains(got, "use go 1.25)\n[running]\n") || !strings.HasSuffix(got, "by user)\n[idle]\n") {
		t.Fatalf("unexpected status sequence:\n%s", got)
	}
	if strings.Count(got, "> fix the test") != 1 {
		t.Fatalf("user message rendered more than once:\n%s", got)
	}
}

func TestCreateRetriesWithOneClientRequestID(t *testing.T) {
	f := &fakeAgentService{createFailures: 1}
	s := startFake(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := createSession(ctx, s, &agentv1.CreateSessionRequest{Message: "hi", ClientRequestId: ptr("key-1")})
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if resp.GetSession().GetSessionId() != "s1" || len(f.creates) != 2 {
		t.Fatalf("expected one retry then s1, got %d calls and %v", len(f.creates), resp)
	}
	for i, req := range f.creates {
		if req.GetClientRequestId() != "key-1" {
			t.Fatalf("attempt %d sent client_request_id %q", i, req.GetClientRequestId())
		}
	}

	f = &fakeAgentService{createFailures: createAttempts}
	s = startFake(t, f)
	if _, err := createSession(ctx, s, &agentv1.CreateSessionRequest{Message: "hi"}); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("expected Unavailable after %d attempts, got %v", createAttempts, err)
	}
	if len(f.creates) != createAttempts {
		t.Fatalf("expected %d attempts, got %d", createAttempts, len(f.creates))
	}
}

func TestWatchReturnsNonRetryableError(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewDepotAgentServiceHandler(agentv1connect.UnimplementedDepotAgentServiceHandler{}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := &session{client: agentv1connect.NewDepotAgentServiceClient(srv.Client(), srv.URL)}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := watchSession(ctx, s, "s1", NewRenderer(&bytes.Buffer{}), false)
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("expected Unimplemented, got %v", err)
	}
}

func TestAttachForwardsLinesAndQuits(t *testing.T) {
	f := &fakeAgentService{streams: [][]*agentv1.WatchSessionResponse{{frame(running, viewOne)}}}
	s := startFake(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	in := strings.NewReader("first\n\n/steer go faster\n/interrupt\n/bogus\n/quit\nnever sent\n")
	var out syncBuffer
	if err := attachSession(ctx, s, "s1", in, &out); err != nil {
		t.Fatalf("attachSession: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("attachSession returned only because the test timed out")
	}

	want := []struct {
		mode    string
		content string
	}{
		{modeFollowup, "first"},
		{modeSteer, "go faster"},
		{"interrupt", ""},
	}
	if len(f.inputs) != len(want) {
		t.Fatalf("expected %d inputs, got %d: %v", len(want), len(f.inputs), f.inputs)
	}
	for i, w := range want {
		if f.inputs[i].GetMode() != w.mode || f.inputs[i].Content != w.content || f.inputs[i].SessionId != "s1" {
			t.Errorf("input %d = %v, want mode %v content %q", i, f.inputs[i], w.mode, w.content)
		}
	}
	if !strings.Contains(out.String(), `unknown command "/bogus"`) {
		t.Fatalf("expected an unknown-command notice, got:\n%s", out.String())
	}
}

// syncBuffer guards a bytes.Buffer shared by the watch goroutine and the input loop.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
