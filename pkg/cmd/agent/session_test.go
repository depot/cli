package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	header    http.Header
	created   *agentv1.CreateSessionRequest
	listed    *agentv1.ListDepotAgentSessionsRequest
	stopped   []string
	watches   []*agentv1.WatchSessionRequest
	frames    [][]*agentv1.WatchSessionResponse
	got       *agentv1.GetSessionResponse
	holdWatch bool
	watchErr  error
	holdFor   time.Duration
	getErr    error
	mu        sync.Mutex
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
	return connect.NewResponse(&agentv1.SendInputResponse{Input: &agentv1.DepotAgentInput{InputId: "dai_1"}}), nil
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
	if f.getErr != nil {
		return nil, f.getErr
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
	if f.holdWatch {
		<-ctx.Done()
	}
	time.Sleep(f.holdFor)
	return f.watchErr
}

func serveAgent(t *testing.T, f *fakeAgent) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewDepotAgentServiceHandler(f))
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	t.Setenv("DEPOT_TOKEN", "tok")
	t.Setenv("DEPOT_ORG_ID", "org-1")
	for _, name := range []string{"GITHUB_RUN_ID", "GITHUB_REPOSITORY", "GITHUB_JOB"} {
		t.Setenv(name, "")
	}
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

func TestTurnOutOfACutViewNeedsToHaveRun(t *testing.T) {
	var cut agentView
	if err := json.Unmarshal([]byte(`{"messages": [{"id": "9", "role": "assistant", "text": "done"}], "truncated": true}`), &cut); err != nil {
		t.Fatal(err)
	}
	var asked agentView
	if err := json.Unmarshal([]byte(viewAsked), &asked); err != nil {
		t.Fatal(err)
	}
	waiting := &turn{afterInput: "dai_1"}
	waiting.settled("running", &cut)
	if done, _, _ := waiting.settled("idle", &cut); done {
		t.Fatal("another turn's reply settled an input that was never seen running")
	}
	waiting.settled("running", &asked)
	if done, reply, _ := waiting.settled("idle", &cut); !done || reply != "done" {
		t.Fatalf("settled = %v, %q after the input ran out of view", done, reply)
	}
}

// Whatever the agent says after a later input answers that input, even when the awaited one never got a reply.
func TestTurnDoesNotTakeALaterInputsReply(t *testing.T) {
	const (
		asked   = `{"id": "3", "role": "user", "inputId": "dai_1", "text": "hello"}`
		working = `{"id": "4", "role": "assistant", "text": "Looking.", "toolCalls": [{"callId": "t"}]}, {"id": "5", "role": "tool", "callId": "t", "isError": true, "text": "interrupted"}`
		later   = `{"id": "6", "role": "user", "inputId": "dai_2", "text": "next"}`
		reply   = `{"id": "7", "role": "assistant", "text": "later reply"}`
		want    = "another input followed before the agent replied"
	)
	for name, tc := range map[string]struct {
		status, view string
		ran          bool
	}{
		"the later input has its reply":      {"idle", asked + "," + working + "," + later + "," + reply, false},
		"the later input is running":         {"running", asked + "," + working + "," + later, false},
		"the later input directly follows":   {"idle", asked + "," + later + "," + reply, false},
		"the session failed on the later":    {"failed", asked + "," + working + "," + later + "," + reply, false},
		"the input has left the cut view":    {"idle", `{"id": "5", "role": "tool", "callId": "t", "text": "interrupted"},` + later + "," + reply, true},
		"the cut view starts at later input": {"idle", later + "," + reply, true},
	} {
		t.Run(name, func(t *testing.T) {
			var view agentView
			if err := json.Unmarshal([]byte(`{"messages": [`+tc.view+`], "truncated": `+strconv.FormatBool(tc.ran)+`}`), &view); err != nil {
				t.Fatal(err)
			}
			done, reply, failure := (&turn{afterInput: "dai_1", ran: tc.ran}).settled(tc.status, &view)
			if !done || reply != "" || failure != want {
				t.Fatalf("settled = %v, %q, %q; want a failure saying %q", done, reply, failure, want)
			}
		})
	}
	var cut agentView
	if err := json.Unmarshal([]byte(`{"messages": [{"id": "4", "role": "assistant", "text": "hi there"},`+later+","+reply+`], "truncated": true}`), &cut); err != nil {
		t.Fatal(err)
	}
	if done, got, failure := (&turn{afterInput: "dai_1", ran: true}).settled("idle", &cut); !done || got != "hi there" || failure != "" {
		t.Fatalf("settled = %v, %q, %q; want the reply left above the later input", done, got, failure)
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
	f := &fakeAgent{frames: [][]*agentv1.WatchSessionResponse{
		{frame("idle", "c1", viewBefore), frame("running", "c2", viewAsked)},
		{frame("idle", "c3", viewReplied)},
	}}
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

func TestWatchExitCodes(t *testing.T) {
	replyGrace = 10 * time.Millisecond
	t.Cleanup(func() { replyGrace = 10 * time.Second })
	interrupted := `{"messages": [{"id": "3", "role": "user", "inputId": "dai_1", "text": "hello"}, {"id": "4", "role": "tool", "toolName": "bash", "text": "killed"}], "tools": []}`
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
		"a later input got the reply": {
			fake: &fakeAgent{frames: [][]*agentv1.WatchSessionResponse{{frame("idle", "c1", strings.Replace(interrupted, `], "tools"`, `, {"id": "5", "role": "user", "inputId": "dai_2", "text": "next"}, {"id": "6", "role": "assistant", "text": "later reply"}], "tools"`, 1))}}},
			code: 1, want: "another input followed before the agent replied",
		},
		"timeout": {
			fake: &fakeAgent{holdWatch: true, frames: [][]*agentv1.WatchSessionResponse{{frame("running", "c1", viewAsked)}}},
			args: []string{"--timeout", "200ms"},
			code: 2, want: "Timed out after 200ms",
		},
	} {
		t.Run(name, func(t *testing.T) {
			serveAgent(t, tc.fake)
			_, _, err := runAgent(t, append([]string{"watch", "das_1", "--after-input", "dai_1"}, tc.args...)...)
			var status cli.StatusError
			if !errors.As(err, &status) || status.StatusCode != tc.code || !strings.Contains(status.Status, tc.want) {
				t.Fatalf("err = %#v, want exit %d with %q", err, tc.code, tc.want)
			}
		})
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
	f := &fakeAgent{getErr: connect.NewError(connect.CodeUnavailable, errors.New("read broke"))}
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
		input, status, view string
		done                bool
		reply               string
	}{
		"idle before the input shows":    {"dai_1", "idle", viewBefore, false, ""},
		"input shown, no reply yet":      {"dai_1", "idle", viewAsked, false, ""},
		"replied":                        {"dai_1", "idle", viewReplied, true, "hi there"},
		"replied, waiting for input":     {"dai_1", "waiting_input", viewReplied, true, "hi there"},
		"still running":                  {"dai_1", "running", viewReplied, false, ""},
		"replied, another input queued":  {"dai_1", "idle", strings.Replace(viewReplied, `"queuedCount": 0`, `"queuedCount": 1`, 1), true, "hi there"},
		"no input: an input queued":      {"", "idle", strings.Replace(viewReplied, `"queuedCount": 0`, `"queuedCount": 1`, 1), false, ""},
		"reply still streaming":          {"dai_1", "idle", strings.Replace(viewReplied, `"tools": []`, `"partial": "and", "tools": []`, 1), false, ""},
		"tool still running":             {"dai_1", "idle", strings.Replace(viewReplied, `"tools": []`, `"tools": [{"callId": "t", "status": "running"}]`, 1), false, ""},
		"no input: idle is enough":       {"", "idle", viewBefore, true, "earlier reply"},
		"replied, then archived":         {"dai_1", "archived", viewReplied, true, "hi there"},
		"replied, then failed":           {"dai_1", "failed", viewReplied, true, "hi there"},
		"replied, a later turn failed":   {"dai_1", "failed", viewLater, true, "hi there"},
		"a later turn is running":        {"dai_1", "running", viewLater, true, "hi there"},
		"a later turn has replied":       {"dai_1", "idle", strings.Replace(viewLater, `]}`, `, {"id": "6", "role": "assistant", "text": "later reply"}]}`, 1), true, "hi there"},
		"no input: idle without a reply": {"", "idle", viewAsked, false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			var view agentView
			if err := json.Unmarshal([]byte(tc.view), &view); err != nil {
				t.Fatal(err)
			}
			done, reply, failure := (&turn{afterInput: tc.input}).settled(tc.status, &view)
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

func TestSendWatchRejectsASubagent(t *testing.T) {
	f := &fakeAgent{}
	serveAgent(t, f)
	_, _, err := runAgent(t, "send", "das_1", "hello", "--subagent", "conv-1", "--watch")
	if err == nil || !strings.Contains(err.Error(), "--subagent") || len(f.watches) != 0 {
		t.Fatalf("err = %v, watches = %d", err, len(f.watches))
	}
}
