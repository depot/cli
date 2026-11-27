package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"github.com/docker/cli/cli"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// fakeAgent is a session API that records what it was asked and plays back scripted watch frames.
type fakeAgent struct {
	agentv1connect.UnimplementedDepotAgentServiceHandler
	header  http.Header
	created *agentv1.CreateSessionRequest
	listed  *agentv1.ListDepotAgentSessionsRequest
	stopped []string
	watches []*agentv1.WatchSessionRequest
	frames  [][]*agentv1.WatchSessionResponse
	got     *agentv1.GetSessionResponse
	// gots are served first, one per read, before got.
	gots []*agentv1.GetSessionResponse
	// getErrEvery makes only every nth read fail with getErr; zero fails them all.
	getErrEvery, gets int
	holdWatch         bool
	sentStatus        string
	watchErr          error
	holdFor           time.Duration
	getErr            error
	// whole is each message's full text by id; wholeErrs fail that many reads first, and wholeAsked records them.
	whole      map[string]*agentv1.GetSessionMessageResponse
	wholeErrs  []error
	wholeAsked []string
	// wholeHangs holds that many whole-message reads open until their caller gives up.
	wholeHangs int
	// inputs are what the API says became of the awaited input, one per read; the last one stays.
	inputs   []*agentv1.GetSessionInputResponse
	inputErr error
	// byWatch serves inputs by how many watch calls there have been, however many frames each one got through.
	byWatch bool
	// afterFrames runs once a watch call has sent its frames.
	afterFrames func(call int)
	mu          sync.Mutex
}

func (f *fakeAgent) GetSessionMessage(ctx context.Context, req *connect.Request[agentv1.GetSessionMessageRequest]) (*connect.Response[agentv1.GetSessionMessageResponse], error) {
	f.mu.Lock()
	hang := f.wholeHangs > 0
	if hang {
		f.wholeHangs--
	}
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wholeAsked = append(f.wholeAsked, req.Msg.GetMessageId())
	if len(f.wholeErrs) > 0 {
		err := f.wholeErrs[0]
		f.wholeErrs = f.wholeErrs[1:]
		return nil, err
	}
	if msg := f.whole[req.Msg.GetMessageId()]; msg != nil {
		return connect.NewResponse(msg), nil
	}
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unimplemented"))
}

func (f *fakeAgent) GetSessionInput(context.Context, *connect.Request[agentv1.GetSessionInputRequest]) (*connect.Response[agentv1.GetSessionInputResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inputErr != nil {
		return nil, f.inputErr
	}
	if len(f.inputs) == 0 {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unimplemented"))
	}
	first := f.inputs[0]
	if f.byWatch {
		first = f.inputs[max(min(len(f.watches), len(f.inputs)), 1)-1]
	} else if len(f.inputs) > 1 {
		f.inputs = f.inputs[1:]
	}
	return connect.NewResponse(first), nil
}

// became is what the API says of an input: its outcome, then the answer's message id or the reason and its detail.
func became(outcome string, more ...string) *agentv1.GetSessionInputResponse {
	resp := &agentv1.GetSessionInputResponse{Outcome: outcome}
	switch {
	case outcome == "answered":
		resp.AnswerMessageId = &more[0]
	case len(more) > 0:
		resp.Reason = &more[0]
		if len(more) > 1 {
			resp.Detail = &more[1]
		}
	}
	return resp
}

func (f *fakeAgent) watchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.watches)
}

func (f *fakeAgent) CreateSession(_ context.Context, req *connect.Request[agentv1.CreateSessionRequest]) (*connect.Response[agentv1.CreateSessionResponse], error) {
	f.header, f.created = req.Header(), req.Msg
	return connect.NewResponse(&agentv1.CreateSessionResponse{
		Session: &agentv1.DepotAgentSession{SessionId: "das_1", Status: "idle"},
		Input:   &agentv1.DepotAgentInput{InputId: "dai_1"},
	}), nil
}

func (f *fakeAgent) SendInput(_ context.Context, req *connect.Request[agentv1.SendInputRequest]) (*connect.Response[agentv1.SendInputResponse], error) {
	return connect.NewResponse(&agentv1.SendInputResponse{Input: &agentv1.DepotAgentInput{InputId: "dai_1", Status: f.sentStatus}}), nil
}

func (f *fakeAgent) ListDepotAgentSessions(_ context.Context, req *connect.Request[agentv1.ListDepotAgentSessionsRequest]) (*connect.Response[agentv1.ListDepotAgentSessionsResponse], error) {
	f.listed = req.Msg
	next := "next"
	return connect.NewResponse(&agentv1.ListDepotAgentSessionsResponse{
		Sessions:      []*agentv1.DepotAgentSession{{SessionId: "das_1", Status: "running", Title: "Fix\x1b[31m the test"}},
		NextPageToken: &next,
	}), nil
}

func (f *fakeAgent) GetSession(context.Context, *connect.Request[agentv1.GetSessionRequest]) (*connect.Response[agentv1.GetSessionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gets++; f.getErr != nil && (f.getErrEvery == 0 || f.gets%f.getErrEvery == 0) {
		return nil, f.getErr
	}
	if len(f.gots) > 0 {
		first := f.gots[0]
		f.gots = f.gots[1:]
		return connect.NewResponse(first), nil
	}
	return connect.NewResponse(f.got), nil
}

func (f *fakeAgent) InterruptSession(_ context.Context, req *connect.Request[agentv1.InterruptSessionRequest]) (*connect.Response[agentv1.InterruptSessionResponse], error) {
	f.stopped = append(f.stopped, "interrupt:"+req.Msg.GetSessionId())
	return connect.NewResponse(&agentv1.InterruptSessionResponse{}), nil
}

func (f *fakeAgent) ArchiveSession(_ context.Context, req *connect.Request[agentv1.ArchiveSessionRequest]) (*connect.Response[agentv1.ArchiveSessionResponse], error) {
	f.stopped = append(f.stopped, "archive:"+req.Msg.GetSessionId())
	return connect.NewResponse(&agentv1.ArchiveSessionResponse{}), nil
}

// Each watch call plays the next batch of frames and then ends, as the API does when its time is up.
func (f *fakeAgent) WatchSession(ctx context.Context, req *connect.Request[agentv1.WatchSessionRequest], stream *connect.ServerStream[agentv1.WatchSessionResponse]) error {
	f.mu.Lock()
	call := len(f.watches)
	f.watches = append(f.watches, req.Msg)
	f.mu.Unlock()
	if call < len(f.frames) {
		for _, frame := range f.frames[call] {
			if err := stream.Send(frame); err != nil {
				return err
			}
		}
	}
	if f.afterFrames != nil {
		f.afterFrames(call)
	}
	if f.holdWatch {
		<-ctx.Done()
	}
	time.Sleep(f.holdFor)
	return f.watchErr
}

// conns keeps the connections a test server accepted, so a test can end them the way a server going away does.
type conns struct {
	net.Listener
	mu       sync.Mutex
	accepted []net.Conn
}

func (c *conns) Accept() (net.Conn, error) {
	conn, err := c.Listener.Accept()
	if err == nil {
		c.mu.Lock()
		c.accepted = append(c.accepted, conn)
		c.mu.Unlock()
	}
	return conn, err
}

// goAway tells every open connection that its streams were all taken and then closes it under them,
// which a client reports as "http2: server sent GOAWAY and closed the connection".
func (c *conns) goAway(t *testing.T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.accepted {
		if err := http2.NewFramer(conn, nil).WriteGoAway(1<<31-1, http2.ErrCodeNo, nil); err != nil {
			t.Errorf("write GOAWAY: %v", err)
		}
		// Closing only the sending side lets the client read what was already sent before it sees the end.
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		} else {
			_ = conn.Close()
		}
	}
	c.accepted = nil
}

func serveAgent(t *testing.T, f *fakeAgent) *conns {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewDepotAgentServiceHandler(f))
	server := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	accepted := &conns{Listener: server.Listener}
	server.Listener = accepted
	server.Start()
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	t.Setenv("DEPOT_TOKEN", "tok")
	t.Setenv("DEPOT_ORG_ID", "org-1")
	for _, name := range []string{"GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_REPOSITORY", "GITHUB_JOB"} {
		t.Setenv(name, "")
	}
	return accepted
}

func runAgent(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewCmdAgent()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func frame(status, cursor, view string) *agentv1.WatchSessionResponse {
	return &agentv1.WatchSessionResponse{Session: &agentv1.DepotAgentSession{SessionId: "das_1", Status: status}, ViewJson: view, Cursor: cursor}
}

const (
	viewBefore = `{"messages": [{"id": "1", "role": "user", "text": "earlier"}, {"id": "2", "role": "assistant", "text": "earlier reply"}], "tools": [], "queuedCount": 0}`
	viewAsked  = `{"messages": [{"id": "1", "role": "user", "text": "earlier"}, {"id": "2", "role": "assistant", "text": "earlier reply"},
		{"id": "3", "role": "user", "inputId": "dai_1", "text": "hello"}], "tools": [], "queuedCount": 0}`
	viewReplied = `{"messages": [{"id": "1", "role": "user", "text": "earlier"}, {"id": "2", "role": "assistant", "text": "earlier reply"},
		{"id": "3", "role": "user", "inputId": "dai_1", "text": "hello"}, {"id": "4", "role": "assistant", "text": "hi there"}], "tools": [], "queuedCount": 0}`
)

const viewLater = `{"messages": [{"id": "3", "role": "user", "inputId": "dai_1", "text": "hello"}, {"id": "4", "role": "assistant", "text": "hi there"},
	{"id": "5", "role": "user", "inputId": "dai_2", "text": "next"}]}`

func TestWatchJSONNeedsAWaitToReport(t *testing.T) {
	f := &fakeAgent{}
	serveAgent(t, f)
	out, _, err := runAgent(t, "watch", "das_1", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "--until-idle or --after-input") || out != "" || f.watchCount() != 0 {
		t.Fatalf("out = %q, err = %v after %d watches; want a refusal before any watch", out, err, f.watchCount())
	}
}

func TestTurnWaitsForAView(t *testing.T) {
	if done, _, _ := (&turn{}).settled("idle", nil); done {
		t.Fatal("an idle session with no view counted as finished")
	}
}

func TestCreateSendsTheMessageAndPrintsTheSession(t *testing.T) {
	f := &fakeAgent{}
	serveAgent(t, f)
	out, _, err := runAgent(t, "create", "hello", "--title", "Greeting", "--model", "anthropic/model-1")
	if err != nil {
		t.Fatal(err)
	}
	req := f.created
	if req.GetMessage() != "hello" || req.GetTitle() != "Greeting" || req.GetModel().GetProvider() != "anthropic" || req.GetModel().GetModelId() != "model-1" || !strings.HasPrefix(req.GetClientRequestId(), "cli:") {
		t.Fatalf("unexpected request %v", req)
	}
	if f.header.Get("Authorization") != "Bearer tok" || f.header.Get("x-depot-org") != "org-1" {
		t.Fatalf("unexpected headers %v", f.header)
	}
	if want := "Created session das_1\nhttps://depot.dev/orgs/org-1/agent-runs/sessions/das_1\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestCreateWatchWaitsForTheReplyAcrossReconnects(t *testing.T) {
	asked, said := []*agentv1.GetSessionInputResponse{became("placed"), became("answered", "4")}, map[string]*agentv1.GetSessionMessageResponse{"4": {Role: "assistant", Text: "hi there"}}
	f := &fakeAgent{frames: [][]*agentv1.WatchSessionResponse{
		{frame("idle", "c1", viewBefore), frame("running", "c2", viewAsked)},
		{frame("idle", "c3", viewReplied)},
	}, inputs: asked, byWatch: true, whole: said}
	serveAgent(t, f)
	out, errOut, err := runAgent(t, "create", "hello", "--watch", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var result watchResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, out)
	}
	if result.Outcome != outcomeSettled || result.Reply != "hi there" || result.InputID != "dai_1" || result.Status != "idle" {
		t.Fatalf("unexpected result %+v", result)
	}
	if want := "[idle]\n[running]\n> hello\nhi there\n[idle]\n"; errOut != want {
		t.Fatalf("transcript = %q, want %q", errOut, want)
	}
	if len(f.watches) != 2 || f.watches[0].AfterCursor != nil || f.watches[1].GetAfterCursor() != "c2" {
		t.Fatalf("watch did not resume from the last cursor: %v", f.watches)
	}
}

// The API ends long-lived connections with a GOAWAY, under a stream that is still open.
func TestWatchReconnectsAfterAGoAway(t *testing.T) {
	retryDelay = time.Millisecond
	t.Cleanup(func() { retryDelay = time.Second })
	frames := [][]*agentv1.WatchSessionResponse{
		{frame("idle", "c1", viewBefore), frame("running", "c2", viewAsked)},
		{frame("idle", "c3", viewReplied)},
	}
	accepted := serveAgent(t, &fakeAgent{holdWatch: true, frames: frames})
	asked, said := []*agentv1.GetSessionInputResponse{became("placed"), became("answered", "4")}, map[string]*agentv1.GetSessionMessageResponse{"4": {Role: "assistant", Text: "hi there"}}

	// What the client makes of it is the error the watch has to count as worth another try.
	stream, err := newCaller("tok", "org-1").client.WatchSession(context.Background(), connect.NewRequest(&agentv1.WatchSessionRequest{SessionId: "das_1"}))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() && stream.Msg().GetCursor() != "c2" {
	}
	accepted.goAway(t)
	for stream.Receive() {
	}
	if err := stream.Err(); err == nil || !strings.Contains(err.Error(), "GOAWAY") || !retryable(err) {
		t.Fatalf("stream ended with %v (retryable %v), want a GOAWAY error the watch retries", err, err != nil && retryable(err))
	}
	_ = stream.Close()

	f := &fakeAgent{holdWatch: true, frames: frames, inputs: asked, byWatch: true, whole: said}
	f.afterFrames = func(call int) {
		if call == 0 {
			accepted.goAway(t)
		}
	}
	accepted = serveAgent(t, f)
	out, errOut, err := runAgent(t, "create", "hello", "--watch", "-o", "json")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, errOut)
	}
	var result watchResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, out)
	}
	if result.Outcome != outcomeSettled || result.Reply != "hi there" {
		t.Fatalf("unexpected result %+v", result)
	}
	// How many of the first stream's frames the client had read when the connection ended is not fixed.
	if !strings.HasSuffix(errOut, "> hello\nhi there\n") && !strings.HasSuffix(errOut, "> hello\nhi there\n[idle]\n") {
		t.Fatalf("transcript = %q, want it to end with the reply", errOut)
	}
	if f.watchCount() != 2 {
		t.Fatalf("watched %d times, want a second stream after the first was cut", f.watchCount())
	}
}

// The view keeps the first 4096 bytes of a message. The reply is read in full, for the transcript and the result.
func TestWatchReadsTheWholeOfALongReply(t *testing.T) {
	retryDelay = time.Millisecond
	t.Cleanup(func() { retryDelay = time.Second })
	long := strings.Repeat("all of it. ", 1000)
	const cut = `{"messages": [{"id": "3", "seq": 3, "role": "user", "inputId": "dai_1", "text": "hello"},
		{"id": "4", "seq": 4, "role": "assistant", "text": "Looking.\n[9 bytes truncated]", "truncatedBytes": 9, "toolCalls": [{"callId": "t", "name": "bash"}]},
		{"id": "5", "seq": 5, "role": "tool", "callId": "t", "toolName": "bash", "text": "[9 bytes truncated]\nok", "truncatedBytes": 9},
		{"id": "6", "seq": 6, "role": "assistant", "text": "all of it. all\n[10986 bytes truncated]", "truncatedBytes": 10986}], "tools": [], "queuedCount": 0}`
	run := func(t *testing.T, f *fakeAgent) (watchResult, string) {
		t.Helper()
		// The second frame is the same view again: a reply already read is not read twice.
		f.frames = [][]*agentv1.WatchSessionResponse{{frame("running", "c1", cut), frame("idle", "c2", cut)}}
		f.inputs = []*agentv1.GetSessionInputResponse{became("placed"), became("answered", "6")}
		serveAgent(t, f)
		out, errOut, err := runAgent(t, "create", "hello", "--watch", "-o", "json")
		if err != nil {
			t.Fatalf("err = %v\n%s", err, errOut)
		}
		var result watchResult
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("stdout is not one JSON object: %v\n%s", err, out)
		}
		return result, errOut
	}
	t.Run("whole", func(t *testing.T) {
		f := &fakeAgent{
			whole:     map[string]*agentv1.GetSessionMessageResponse{"6": {Role: "assistant", Text: long}},
			wholeErrs: []error{connect.NewError(connect.CodeUnavailable, errors.New("try again"))},
		}
		result, transcript := run(t, f)
		if result.Reply != long {
			t.Fatalf("reply is %d bytes ending %q, want all %d", len(result.Reply), result.Reply[max(0, len(result.Reply)-40):], len(long))
		}
		if !strings.Contains(transcript, strings.TrimSpace(long)+"\n") || strings.Contains(transcript, "10986 bytes truncated") {
			t.Fatalf("transcript does not carry the whole reply:\n%s", truncate(transcript))
		}
		if got := strings.Join(f.wholeAsked, ","); got != "6,6" {
			t.Fatalf("read messages %q, want the reply alone, once after one failed try", got)
		}
	})
	t.Run("read on a later frame after the first one could not", func(t *testing.T) {
		down := connect.NewError(connect.CodeUnavailable, errors.New("try again"))
		f := &fakeAgent{whole: map[string]*agentv1.GetSessionMessageResponse{"6": {Role: "assistant", Text: long}}, wholeErrs: []error{down, down, down}}
		result, transcript := run(t, f)
		if result.Reply != long || !strings.Contains(transcript, "(the whole reply, read on a later try)\n"+strings.TrimSpace(long)) {
			t.Fatalf("reply is %d bytes, want all %d, and the transcript to carry it:\n%s", len(result.Reply), len(long), truncate(transcript))
		}
		if got := strings.Join(f.wholeAsked, ","); got != "6,6,6,6" {
			t.Fatalf("read messages %q, want three failed tries and one read on the next frame", got)
		}
	})
	t.Run("past the API's own limit", func(t *testing.T) {
		f := &fakeAgent{whole: map[string]*agentv1.GetSessionMessageResponse{"6": {Role: "assistant", Text: long, TruncatedBytes: 7}}}
		if result, _ := run(t, f); result.Reply != long+"\n[7 bytes truncated]" {
			t.Fatalf("reply ends %q, want the note that it is still cut", result.Reply[len(result.Reply)-40:])
		}
	})
}

func TestRendererForgetsAWholeReplyOnceItLeavesTheView(t *testing.T) {
	reads := 0
	r := NewRenderer(io.Discard).Whole(func(string) (string, error) { reads++; return "all of it", nil })
	const cut = `{"messages": [{"id": "6", "seq": 6, "role": "assistant", "text": "all\n[6 bytes truncated]", "truncatedBytes": 6}]}`
	for _, viewJSON := range []string{cut, cut} {
		if view, err := r.Render(nil, viewJSON); err != nil || view.Messages[0].Text != "all of it" {
			t.Fatalf("view = %+v, err = %v, want the whole reply", view, err)
		}
	}
	if _, err := r.Render(nil, `{"messages": [{"id": "9", "seq": 9, "role": "user", "text": "next"}]}`); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || len(r.full) != 0 {
		t.Fatalf("%d reads and %d texts kept, want one read and nothing kept past the view", reads, len(r.full))
	}
}

func TestRendererReadsAReplyAgainWithoutRestartingTheNextTurnsStream(t *testing.T) {
	var out strings.Builder
	fails := 2
	r := NewRenderer(&out).Whole(func(string) (string, error) {
		if fails--; fails >= 0 {
			return "", connect.NewError(connect.CodeUnavailable, errors.New("try again"))
		}
		return "all of it", nil
	})
	const cut = `{"id": "6", "seq": 6, "role": "assistant", "text": "all\n[6 bytes truncated]", "truncatedBytes": 6}`
	const next = cut + `, {"id": "7", "seq": 7, "role": "user", "text": "next"}`
	for _, viewJSON := range []string{
		`{"messages": [` + cut + `]}`,
		`{"messages": [` + next + `], "partial": "second ans"}`,
		`{"messages": [` + next + `], "partial": "second answer"}`,
		`{"messages": [` + next + `, {"id": "8", "seq": 8, "role": "assistant", "text": "second answer."}]}`,
	} {
		if _, err := r.Render(nil, viewJSON); err != nil {
			t.Fatal(err)
		}
	}
	want := "all\n[6 bytes truncated]\n(the rest of this reply could not be read: unavailable: try again)\n> next\nsecond ans\n" +
		"(the whole reply, read on a later try)\nall of it\nsecond answer.\n"
	if out.String() != want {
		t.Fatalf("transcript = %q, want %q", out.String(), want)
	}
}

func TestWatchTimesOutWhenTheWholeReplyReadOutlastsTheWait(t *testing.T) {
	const cut = `{"messages": [{"id": "3", "seq": 3, "role": "user", "inputId": "dai_1", "text": "hello"},
		{"id": "6", "seq": 6, "role": "assistant", "text": "all\n[6 bytes truncated]", "truncatedBytes": 6}], "tools": []}`
	f := &fakeAgent{wholeHangs: 100, frames: [][]*agentv1.WatchSessionResponse{{frame("idle", "c1", cut)}}, inputs: []*agentv1.GetSessionInputResponse{became("answered", "6")}}
	serveAgent(t, f)
	out, _, err := runAgent(t, "watch", "das_1", "--after-input", "dai_1", "--timeout", "200ms", "-o", "json")
	var status cli.StatusError
	if !errors.As(err, &status) || status.StatusCode != timeoutExitCode || !strings.Contains(out, `"outcome": "timeout"`) || strings.Contains(out, `"reply"`) {
		t.Fatalf("err = %#v, want a timeout with no reply:\n%s", err, out)
	}
}

func TestWatchReadsTheWholeReplyBeforeAWaitThatEndsOnItsFrame(t *testing.T) {
	retryDelay, wholeMessageTimeout = time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { retryDelay, wholeMessageTimeout = time.Second, 30*time.Second })
	const cut = `{"messages": [{"id": "3", "seq": 3, "role": "user", "inputId": "dai_1", "text": "hello"},
		{"id": "6", "seq": 6, "role": "assistant", "text": "all\n[6 bytes truncated]", "truncatedBytes": 6}], "tools": []}`
	// An earlier turn's cut reply, with the awaited input still queued: idle, yet the wait goes on.
	const older = `{"messages": [{"id": "1", "seq": 1, "role": "user", "inputId": "dai_0", "text": "before"},
		{"id": "2", "seq": 2, "role": "assistant", "text": "ear\n[9 bytes truncated]", "truncatedBytes": 9}], "tools": [], "queuedCount": 1}`
	// The awaited input has its reply, and the session is already on the next input.
	next := strings.Replace(cut, `], "tools"`, `, {"id": "7", "seq": 7, "role": "user", "inputId": "dai_2", "text": "next"}], "tools"`, 1)
	// The next turn's reply is cut to the very same text as the awaited one.
	twin := strings.Replace(next, `], "tools"`, `, {"id": "8", "seq": 8, "role": "assistant", "text": "all\n[6 bytes truncated]", "truncatedBytes": 6}], "tools"`, 1)
	down := connect.NewError(connect.CodeUnavailable, errors.New("try again"))
	for name, f := range map[string]*fakeAgent{
		"a later reply is cut to the same text":    {wholeErrs: []error{down, down, down, down, down, down}, frames: [][]*agentv1.WatchSessionResponse{{frame("idle", "c1", twin)}}},
		"every try of the first read failed":       {wholeErrs: []error{down, down, down}, frames: [][]*agentv1.WatchSessionResponse{{frame("idle", "c1", cut)}}},
		"the first try hung":                       {wholeHangs: 1, frames: [][]*agentv1.WatchSessionResponse{{frame("idle", "c1", cut)}}},
		"the session is on to the next input":      {wholeErrs: []error{down, down, down}, frames: [][]*agentv1.WatchSessionResponse{{frame("running", "c1", next)}}},
		"an idle frame that does not end the wait": {wholeErrs: []error{down, down, down}, frames: [][]*agentv1.WatchSessionResponse{{frame("idle", "c1", older), frame("running", "c2", older), frame("idle", "c3", cut)}}},
	} {
		t.Run(name, func(t *testing.T) {
			f.whole = map[string]*agentv1.GetSessionMessageResponse{"6": {Role: "assistant", Text: "all of it"}, "2": {Role: "assistant", Text: "earlier, whole"}, "8": {Role: "assistant", Text: "the next turn's"}}
			most := len(f.wholeErrs) + 2
			f.inputs = []*agentv1.GetSessionInputResponse{became("answered", "6")}
			if len(f.frames[0]) > 1 {
				f.inputs = []*agentv1.GetSessionInputResponse{became("pending"), became("pending"), became("answered", "6")}
			}
			serveAgent(t, f)
			out, errOut, err := runAgent(t, "watch", "das_1", "--after-input", "dai_1", "--until-idle", "-o", "json")
			if err != nil || !strings.Contains(out, `"reply": "all of it"`) {
				t.Fatalf("err = %v, want the whole reply:\n%s\n%s", err, out, errOut)
			}
			// A hung try leaves the first read its other tries; only a read that used them all is read again.
			if later := strings.Contains(errOut, "read on a later try"); later != (len(f.wholeAsked) >= 4) || len(f.wholeAsked) > most {
				t.Fatalf("read again = %v after reads %v:\n%s", later, f.wholeAsked, errOut)
			}
		})
	}
}

// A watch on an input ends on what the API says became of it, whatever the view shows and whoever wrote since.
func TestWatchEndsOnWhatTheAPISaysBecameOfTheInput(t *testing.T) {
	later := strings.Replace(viewReplied, `], "tools"`, `, {"id": "5", "role": "user", "inputId": "dai_2", "text": "next"}, {"id": "6", "role": "assistant", "text": "later reply"}], "tools"`, 1)
	hi := map[string]*agentv1.GetSessionMessageResponse{"4": {Role: "assistant", Text: "hi there"}, "9": {Role: "assistant", Text: "read by its id"}, "0": {Role: "assistant", Text: " "}}
	for name, tc := range map[string]struct {
		status, view string
		input        *agentv1.GetSessionInputResponse
		code         int
		want         string
	}{
		"answered":                              {"idle", viewReplied, became("answered", "4"), 0, "hi there"},
		"answered, a later turn has replied":    {"idle", later, became("answered", "4"), 0, "hi there"},
		"answered, a later turn is running":     {"running", viewLater, became("answered", "4"), 0, "hi there"},
		"answered, then the session failed":     {"failed", viewReplied, became("answered", "4"), 0, "hi there"},
		"answered, the reply out of the view":   {"idle", viewBefore, became("answered", "9"), 0, "read by its id"},
		"an empty reply":                        {"idle", viewReplied, became("answered", "0"), 1, "the agent's reply was empty"},
		"stopped":                               {"idle", viewAsked, became("unanswered", "aborted"), 1, "the turn was stopped before the agent replied"},
		"model error":                           {"idle", viewAsked, became("unanswered", "model_error", "overloaded"), 1, "the agent's reply was an error: overloaded"},
		"no answer for another reason":          {"idle", viewAsked, became("unanswered", "reset"), 1, "the turn ended without a reply: reset"},
		"cancelled unread":                      {"archived", viewBefore, became("withdrawn"), 1, "the input was cancelled before the agent took it"},
		"placed in a session that failed":       {"failed", viewAsked, became("placed"), 1, "the session failed"},
		"pending in a session that is archived": {"archived", viewBefore, became("pending"), 1, "the session was archived"},
		"pending while the session is idle":     {"idle", viewBefore, became("pending"), 2, "Timed out after 200ms"},
		"placed, a later message in the view":   {"running", later, became("placed"), 2, "Timed out after 200ms"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeAgent{holdWatch: true, frames: [][]*agentv1.WatchSessionResponse{{frame(tc.status, "c1", tc.view)}}, inputs: []*agentv1.GetSessionInputResponse{tc.input}, whole: hi}
			serveAgent(t, f)
			out, _, err := runAgent(t, "watch", "das_1", "--after-input", "dai_1", "--timeout", "200ms", "-o", "json")
			var status cli.StatusError
			if tc.code == 0 {
				var result watchResult
				if err != nil || json.Unmarshal([]byte(out), &result) != nil || result.Outcome != outcomeSettled || result.Reply != tc.want {
					t.Fatalf("err = %v, out = %s; want the reply %q", err, out, tc.want)
				}
			} else if !errors.As(err, &status) || status.StatusCode != tc.code || !strings.Contains(status.Status, tc.want) {
				t.Fatalf("err = %#v, want exit %d with %q", err, tc.code, tc.want)
			}
		})
	}
}

func TestWatchUntilIdleExitCodes(t *testing.T) {
	replyGrace = 10 * time.Millisecond
	t.Cleanup(func() { replyGrace = 10 * time.Second })
	interrupted := `{"messages": [{"id": "3", "role": "user", "inputId": "dai_1", "text": "hello"}, {"id": "4", "role": "tool", "toolName": "bash", "text": "killed"}], "tools": []}`
	stopped := `{"messages": [{"id": "3", "role": "user", "inputId": "dai_1", "text": "hello"}, {"id": "4", "role": "assistant", "text": "Let me look.", "toolCalls": [{"callId": "t", "name": "bash"}]}], "tools": []}`
	for name, tc := range map[string]struct {
		fake *fakeAgent
		args []string
		code int
		want string
	}{
		"failed session": {
			fake: &fakeAgent{frames: [][]*agentv1.WatchSessionResponse{{frame("failed", "c1", viewAsked)}}},
			code: 1, want: "the session failed",
		},
		"error reply": {
			fake: &fakeAgent{frames: [][]*agentv1.WatchSessionResponse{{frame("idle", "c1", `{"messages": [{"id": "3", "role": "user", "inputId": "dai_1", "text": "hello"}, {"id": "4", "role": "assistant", "isError": true, "text": "overloaded"}]}`)}}},
			code: 1, want: "the agent's reply was an error: overloaded",
		},
		"turn without a reply": {
			fake: &fakeAgent{
				frames: [][]*agentv1.WatchSessionResponse{{frame("idle", "c1", interrupted)}},
				got:    &agentv1.GetSessionResponse{Session: &agentv1.DepotAgentSession{Status: "idle"}, ViewJson: interrupted},
			},
			code: 1, want: "the turn ended without a reply",
		},
		"turn that stopped at a tool call": {
			fake: &fakeAgent{
				frames: [][]*agentv1.WatchSessionResponse{{frame("idle", "c1", stopped)}},
				got:    &agentv1.GetSessionResponse{Session: &agentv1.DepotAgentSession{Status: "idle"}, ViewJson: stopped},
			},
			code: 1, want: "the turn ended without a reply",
		},
		"timeout": {
			fake: &fakeAgent{holdWatch: true, frames: [][]*agentv1.WatchSessionResponse{{frame("running", "c1", viewAsked)}}},
			args: []string{"--timeout", "200ms"},
			code: 2, want: "Timed out after 200ms",
		},
	} {
		t.Run(name, func(t *testing.T) {
			serveAgent(t, tc.fake)
			_, _, err := runAgent(t, append([]string{"watch", "das_1", "--until-idle"}, tc.args...)...)
			var status cli.StatusError
			if !errors.As(err, &status) || status.StatusCode != tc.code || !strings.Contains(status.Status, tc.want) {
				t.Fatalf("err = %#v, want exit %d with %q", err, tc.code, tc.want)
			}
		})
	}
}

func TestWatchWithoutAWaitEndsOnlyOnATimeoutThatWasAskedFor(t *testing.T) {
	idle := [][]*agentv1.WatchSessionResponse{{frame("idle", "c1", viewAsked)}}
	serveAgent(t, &fakeAgent{holdWatch: true, frames: idle})
	started := time.Now()
	out, _, err := runAgent(t, "watch", "das_1", "--timeout", "200ms")
	var status cli.StatusError
	if !errors.As(err, &status) || status.StatusCode != timeoutExitCode || status.Status != "Timed out after 200ms watching session das_1 (idle)" {
		t.Fatalf("err = %#v, want exit %d naming the timeout", err, timeoutExitCode)
	}
	if time.Since(started) > 5*time.Second || !strings.Contains(out, "hello") {
		t.Fatalf("the watch took %s and printed %q, want the transcript and a prompt exit", time.Since(started), out)
	}

	// With no --timeout the 30m default of a wait does not apply: the watch is still running when it is interrupted.
	defaultWatchTimeout = 20 * time.Millisecond
	t.Cleanup(func() { defaultWatchTimeout = 30 * time.Minute })
	serveAgent(t, &fakeAgent{holdWatch: true, frames: idle})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	cmd := NewCmdAgent()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"watch", "das_1"})
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation, not a timeout", err)
	}
}

func TestWatchGivesUpOnAnErrorThatKeepsHappening(t *testing.T) {
	retryDelay = time.Millisecond
	t.Cleanup(func() { retryDelay = time.Second })
	f := &fakeAgent{watchErr: connect.NewError(connect.CodeInternal, errors.New("projection broke"))}
	serveAgent(t, f)
	_, _, err := runAgent(t, "watch", "das_1", "--until-idle")
	if err == nil || !strings.Contains(err.Error(), "projection broke") || len(f.watches) != maxWatchFailures {
		t.Fatalf("err = %v after %d watches, want the server's error after %d", err, len(f.watches), maxWatchFailures)
	}
}

func TestWatchReplaysAFrameWhoseSessionReadFailed(t *testing.T) {
	retryDelay, replyGrace = time.Millisecond, time.Millisecond
	t.Cleanup(func() { retryDelay, replyGrace = time.Second, 10*time.Second })
	unanswered := []*agentv1.WatchSessionResponse{frame("idle", "c1", viewAsked)}
	f := &fakeAgent{inputErr: connect.NewError(connect.CodeUnavailable, errors.New("read broke"))}
	for range maxWatchFailures {
		f.frames = append(f.frames, unanswered)
	}
	serveAgent(t, f)
	_, _, err := runAgent(t, "watch", "das_1", "--after-input", "dai_1")
	if err == nil || !strings.Contains(err.Error(), "read broke") || f.watchCount() != maxWatchFailures {
		t.Fatalf("err = %v after %d watches, want the read's error after %d", err, f.watchCount(), maxWatchFailures)
	}
	for _, req := range f.watches {
		if req.GetAfterCursor() != "" {
			t.Fatalf("the watch moved past a frame it had not finished: %v", req)
		}
	}
}

func TestWatchKeepsReconnectingAStreamThatWasHeldOpen(t *testing.T) {
	retryDelay, streamHeld = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { retryDelay, streamHeld = time.Second, 30*time.Second })
	f := &fakeAgent{holdFor: 20 * time.Millisecond, watchErr: connect.NewError(connect.CodeUnavailable, errors.New("stream cut"))}
	serveAgent(t, f)
	_, _, err := runAgent(t, "watch", "das_1", "--until-idle", "--timeout", "500ms")
	var status cli.StatusError
	if !errors.As(err, &status) || status.StatusCode != timeoutExitCode || f.watchCount() <= maxWatchFailures {
		t.Fatalf("err = %v after %d watches, want a timeout after more than %d", err, f.watchCount(), maxWatchFailures)
	}
}

func TestTurnSettled(t *testing.T) {
	for name, tc := range map[string]struct {
		status, view string
		done         bool
		reply        string
	}{
		"idle is enough":        {"idle", viewBefore, true, "earlier reply"},
		"waiting for input":     {"waiting_input", viewReplied, true, "hi there"},
		"still running":         {"running", viewReplied, false, ""},
		"an input queued":       {"idle", strings.Replace(viewReplied, `"queuedCount": 0`, `"queuedCount": 1`, 1), false, ""},
		"reply still streaming": {"idle", strings.Replace(viewReplied, `"tools": []`, `"partial": "and", "tools": []`, 1), false, ""},
		"tool still running":    {"idle", strings.Replace(viewReplied, `"tools": []`, `"tools": [{"callId": "t", "status": "running"}]`, 1), false, ""},
		"idle without a reply":  {"idle", viewAsked, false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			var view agentView
			if err := json.Unmarshal([]byte(tc.view), &view); err != nil {
				t.Fatal(err)
			}
			done, reply, failure := (&turn{}).settled(tc.status, &view)
			if done != tc.done || reply != tc.reply || failure != "" {
				t.Fatalf("settled = %v, %q, %q; want %v, %q", done, reply, failure, tc.done, tc.reply)
			}
		})
	}
}

func TestListPrintsATableAndTheNextPage(t *testing.T) {
	f := &fakeAgent{}
	serveAgent(t, f)
	out, errOut, err := runAgent(t, "list", "--limit", "5", "--page-token", "prev")
	if err != nil {
		t.Fatal(err)
	}
	if f.listed.GetPageSize() != 5 || f.listed.GetPageToken() != "prev" {
		t.Fatalf("unexpected request %v", f.listed)
	}
	if !strings.Contains(out, "das_1") || !strings.Contains(out, "running") || strings.Contains(out, "\x1b") {
		t.Fatalf("unexpected table %q", out)
	}
	if !strings.Contains(errOut, "--page-token next") {
		t.Fatalf("next page not offered: %q", errOut)
	}
}

func TestLogsPrintsTheTranscriptOrTheView(t *testing.T) {
	f := &fakeAgent{got: &agentv1.GetSessionResponse{Session: &agentv1.DepotAgentSession{SessionId: "das_1", Status: "idle"}, ViewJson: viewReplied, ViewSeq: 7}}
	serveAgent(t, f)
	out, _, err := runAgent(t, "logs", "das_1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "> earlier\nearlier reply\n> hello\nhi there\n[idle]\n"; out != want {
		t.Fatalf("transcript = %q, want %q", out, want)
	}
	out, _, err = runAgent(t, "logs", "das_1", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Session struct{ SessionID string }
		ViewSeq int
		View    agentView
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Session.SessionID != "das_1" || got.ViewSeq != 7 || len(got.View.Messages) != 4 {
		t.Fatalf("unexpected JSON (%v): %s", err, out)
	}
}

func TestLogsReadsTheWholeOfALongReply(t *testing.T) {
	long := strings.Repeat("all of it. ", 1000)
	const cut = `{"messages": [{"id": "3", "seq": 3, "role": "user", "inputId": "dai_1", "text": "hello"},
		{"id": "5", "seq": 5, "role": "tool", "callId": "t", "toolName": "bash", "text": "[9 bytes truncated]\nok", "truncatedBytes": 9},
		{"id": "6", "seq": 6, "role": "assistant", "text": "all of it. all\n[10986 bytes truncated]", "truncatedBytes": 10986}], "tools": []}`
	got := &agentv1.GetSessionResponse{Session: &agentv1.DepotAgentSession{SessionId: "das_1", Status: "idle"}, ViewJson: cut, ViewSeq: 7}
	f := &fakeAgent{got: got, whole: map[string]*agentv1.GetSessionMessageResponse{"6": {Role: "assistant", Text: long}}}
	serveAgent(t, f)
	out, _, err := runAgent(t, "logs", "das_1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, strings.TrimSpace(long)+"\n") || strings.Contains(out, "10986 bytes truncated") {
		t.Fatalf("transcript does not carry the whole reply:\n%s", truncate(out))
	}
	if asked := strings.Join(f.wholeAsked, ","); asked != "6" {
		t.Fatalf("read messages %q, want the reply alone", asked)
	}
	out, _, err = runAgent(t, "logs", "das_1", "-o", "json")
	if err != nil || !strings.Contains(out, "10986 bytes truncated") || len(f.wholeAsked) != 1 {
		t.Fatalf("-o json should print the view as returned (%v), read %v:\n%s", err, f.wholeAsked, truncate(out))
	}
	// An API without the read keeps the cut text and says so.
	old := &fakeAgent{got: got}
	serveAgent(t, old)
	out, _, err = runAgent(t, "logs", "das_1")
	if err != nil || !strings.Contains(out, "[10986 bytes truncated]") || !strings.Contains(out, "(the rest of this reply could not be read: ") {
		t.Fatalf("unexpected transcript from an API without the read (%v):\n%s", err, truncate(out))
	}
}

func TestStopInterruptsOrArchives(t *testing.T) {
	f := &fakeAgent{}
	serveAgent(t, f)
	first, _, err := runAgent(t, "stop", "das_1")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := runAgent(t, "stop", "das_1", "--archive")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.stopped, ",") != "interrupt:das_1,archive:das_1" || first != "Interrupted session das_1\n" || second != "Archived session das_1\n" {
		t.Fatalf("stopped %v, output %q %q", f.stopped, first, second)
	}
}

func TestSendFailsOnAReplayedInputThatWasCancelled(t *testing.T) {
	for output, want := range map[string]string{"text": "", "json": `"cancelled"`} {
		t.Run(output, func(t *testing.T) {
			serveAgent(t, &fakeAgent{sentStatus: "cancelled"})
			stdout, _, err := runAgent(t, "send", "das_1", "hello", "--client-request-id", "again", "-o", output)
			if err == nil || !strings.Contains(err.Error(), "dai_1 was not delivered") || strings.Contains(stdout, "Sent") || !strings.Contains(stdout, want) {
				t.Fatalf("err = %v, want a failure that says nothing was sent:\n%s", err, stdout)
			}
		})
	}
}

func TestSendWatchFailsAtOnceOnAReplayedInputThatWasCancelled(t *testing.T) {
	f := &fakeAgent{sentStatus: "cancelled"}
	serveAgent(t, f)
	stdout, _, err := runAgent(t, "send", "das_1", "hello", "--client-request-id", "again", "--watch", "-o", "json")
	var status cli.StatusError
	if !errors.As(err, &status) || status.StatusCode != 1 || !strings.Contains(err.Error(), "cancelled") || len(f.watches) != 0 {
		t.Fatalf("err = %v, watches = %d; want exit 1 without a watch", err, len(f.watches))
	}
	if !strings.Contains(stdout, `"outcome": "failed"`) || !strings.Contains(stdout, `"inputId": "dai_1"`) {
		t.Fatalf("result = %s", stdout)
	}
}

func TestSendWatchRejectsASubagent(t *testing.T) {
	f := &fakeAgent{}
	serveAgent(t, f)
	_, _, err := runAgent(t, "send", "das_1", "hello", "--subagent", "conv-1", "--watch")
	if err == nil || !strings.Contains(err.Error(), "--subagent") || len(f.watches) != 0 {
		t.Fatalf("err = %v, watches = %d", err, len(f.watches))
	}
}

func TestWatchReportsACancelledContext(t *testing.T) {
	for _, args := range [][]string{{"watch", "das_1"}, {"watch", "das_1", "--until-idle"}} {
		serveAgent(t, &fakeAgent{holdWatch: true, frames: [][]*agentv1.WatchSessionResponse{{frame("running", "c1", viewAsked)}}})
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		cmd := NewCmdAgent()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("%v: err = %v, want the cancellation, not a finished watch", args, err)
		}
	}
}
