package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/docker/cli/cli"
	"github.com/spf13/cobra"
)

const (
	outcomeSettled = "settled"
	outcomeFailed  = "failed"
	outcomeTimeout = "timeout"

	// The command's exit code when the wait ran out of time; a failed turn exits 1.
	timeoutExitCode = 2
)

// How long a wait for a reply or for an idle session lasts when --timeout is not given.
var defaultWatchTimeout = 30 * time.Minute

// How long an idle session whose turn has no reply is given to show one before the turn counts as failed.
var replyGrace = 10 * time.Second

// How long an idle session is given to pick up an input that its cut view does not show, and how often it is
// read meanwhile. Nothing tells an input sent a moment ago from one whose turn the view has long dropped.
var (
	pickupGrace = 2 * time.Minute
	pickupPoll  = 5 * time.Second
)

// A watch that fails this many times in a row gives up with the last error. A stream that brought a frame
// or stayed open for streamHeld is not such a failure: a quiet session's stream is cut from time to time.
// Each retry waits one retryDelay longer than the one before. streamHeld is longer than replyGrace,
// so a session read that keeps failing after the grace wait still counts.
const maxWatchFailures = 10

// wholeMessageTries and wholeMessageTimeout bound the read of one reply the view cut short, and each try of it.
const wholeMessageTries = 3

var (
	retryDelay          = time.Second
	streamHeld          = 30 * time.Second
	wholeMessageTimeout = 30 * time.Second
)

type watchOptions struct {
	// afterInput waits for the reply to this input instead of for the session to be idle at all.
	afterInput string
	untilIdle  bool
	timeout    time.Duration
	// url is the session's URL when the API gave one; otherwise it is built from the organization.
	url string
	// base is the seq of the session's newest message before afterInput was sent, when the caller knows it.
	base *int64
	// did is what the command did to the session before watching it, said when the watch then fails.
	did string
}

// beforeFirst is the base of a session's first input: no message precedes it.
func beforeFirst() *int64 {
	base := int64(-1)
	return &base
}

// newestSeq reads the seq of the session's newest message, for an input about to be sent. It is nil when the
// read fails or the view is cut to nothing, and the watch then takes its base from the first frame.
func newestSeq(ctx context.Context, c *caller, sessionID string) *int64 {
	resp, err := c.client.GetSession(ctx, authed(c, &agentv1.GetSessionRequest{SessionId: sessionID}))
	if err != nil {
		return nil
	}
	// A session has no view until its agent first reports one, and no messages until then either.
	raw := resp.Msg.GetViewJson()
	if raw == "" {
		return beforeFirst()
	}
	var view agentView
	if json.Unmarshal([]byte(raw), &view) != nil {
		return nil
	}
	t := &turn{}
	t.mark(&view)
	return t.base
}

type watchResult struct {
	SessionID string `json:"sessionId"`
	InputID   string `json:"inputId,omitempty"`
	Status    string `json:"status"`
	Outcome   string `json:"outcome"`
	Reply     string `json:"reply,omitempty"`
	Error     string `json:"error,omitempty"`
	// Joined counts the later messages that landed in the awaited turn, so its reply answers them too.
	Joined int    `json:"joined,omitempty"`
	URL    string `json:"url,omitempty"`
}

func newWatch() *cobra.Command {
	f := &sessionFlags{}
	o := watchOptions{}
	cmd := &cobra.Command{
		Use:   "watch <session-id>",
		Short: "Follow an agent session as it works",
		Long: `Print a session's messages and tool calls as they happen.

Without --until-idle or --after-input the watch does not end when the session goes idle: it runs until interrupted,
until the session is archived, or until --timeout if one is given, which exits 2.
With --until-idle it runs until the session has finished its work, for at most --timeout (30m unless set). The exit code
is 0 when the session ended on a reply, 1 when it failed, was archived, or ended on an error or without a reply, and 2 on --timeout.
--after-input waits for the reply to one input, so a session that is still idle from its last turn does not count.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.caller(cmd)
			if err != nil {
				return err
			}
			if o.afterInput != "" {
				o.untilIdle = true
			}
			if f.output == "json" && !o.untilIdle {
				return errors.New("-o json prints the result of a wait: add --until-idle or --after-input")
			}
			// The default bounds a wait; a watch that only follows the session ends on a timeout the caller asked for.
			if !o.untilIdle && !cmd.Flags().Changed("timeout") {
				o.timeout = 0
			}
			return watchAndReport(cmd, c, f.output, args[0], o)
		},
	}
	f.add(cmd)
	cmd.Flags().BoolVar(&o.untilIdle, "until-idle", false, "Exit once the session has finished its work")
	cmd.Flags().StringVar(&o.afterInput, "after-input", "", "Exit once this input (dai_...) has its reply; implies --until-idle")
	cmd.Flags().DurationVar(&o.timeout, "timeout", defaultWatchTimeout, "Exit with code 2 after this long; without --until-idle or --after-input, only when this flag is given")
	return cmd
}

// addWatchFlags gives a command that sends an input the flags to wait for its reply.
func addWatchFlags(cmd *cobra.Command, watch *bool, timeout *time.Duration) {
	cmd.Flags().BoolVar(watch, "watch", false, "Follow the session until the agent has replied; the exit code is 1 on a failed turn and 2 on --timeout")
	cmd.Flags().DurationVar(timeout, "timeout", defaultWatchTimeout, "How long --watch waits for the reply")
}

// watchAndReport follows the session, printing the transcript to stdout,
// or with JSON output the transcript to stderr and one result object to stdout.
func watchAndReport(cmd *cobra.Command, c *caller, output, sessionID string, o watchOptions) error {
	transcript := cmd.OutOrStdout()
	if output == "json" {
		transcript = cmd.ErrOrStderr()
	}
	result, err := watchSession(cmd.Context(), c, sessionID, NewRenderer(transcript).From(o.afterInput), o)
	if err != nil {
		err = c.refused(err, "WatchSession")
		if o.did != "" && cmd.Context().Err() == nil {
			err = fmt.Errorf("%w\nSession %s %s and is not being watched; read its reply with `depot agent logs %s`", err, sessionID, o.did, sessionID)
		}
		return err
	}
	if !o.untilIdle {
		if result.Outcome == outcomeTimeout {
			return cli.StatusError{Status: fmt.Sprintf("Timed out after %s watching session %s (%s)", o.timeout, sessionID, result.Status), StatusCode: timeoutExitCode}
		}
		return nil
	}
	return report(cmd, c, output, sessionID, o, result)
}

// report prints the result of a wait as JSON when asked to, and turns its outcome into the exit code.
func report(cmd *cobra.Command, c *caller, output, sessionID string, o watchOptions, result watchResult) error {
	if result.URL = o.url; result.URL == "" {
		result.URL = sessionURL(c.org, sessionID)
	}
	if output == "json" {
		out, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(out)); err != nil {
			return err
		}
	}
	switch result.Outcome {
	case outcomeSettled:
		return nil
	case outcomeTimeout:
		return cli.StatusError{Status: fmt.Sprintf("Timed out after %s waiting for session %s (%s)", o.timeout, sessionID, result.Status), StatusCode: timeoutExitCode}
	default:
		return cli.StatusError{Status: fmt.Sprintf("Session %s: %s", sessionID, result.Error), StatusCode: 1}
	}
}

// watchSession streams a session through r, reconnecting when the server ends the stream,
// until ctx is cancelled, the session is archived, or the wait the options ask for is over.
func watchSession(ctx context.Context, c *caller, sessionID string, r *Renderer, o watchOptions) (watchResult, error) {
	result := watchResult{SessionID: sessionID, InputID: o.afterInput}
	deadline := ctx
	if o.timeout > 0 {
		var cancel context.CancelFunc
		deadline, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}
	r.Whole(func(messageID string) (string, error) { return wholeMessage(deadline, c, sessionID, messageID) })
	t := &turn{afterInput: o.afterInput, base: o.base}
	cursor, failures := "", 0
	for {
		before, opened := cursor, time.Now()
		done, err := watchOnce(deadline, c, sessionID, r, o, t, &cursor, &result)
		if done {
			return result, nil
		}
		if ctx.Err() != nil {
			return result, fmt.Errorf("watch session %s: %w", sessionID, ctx.Err())
		}
		if deadline.Err() != nil {
			result.Outcome = outcomeTimeout
			return result, nil
		}
		if err == nil || cursor != before || time.Since(opened) >= streamHeld {
			failures = 0
		}
		if err != nil {
			if failures++; !retryable(err) || failures >= maxWatchFailures {
				return result, err
			}
		}
		select {
		case <-deadline.Done():
		case <-time.After(retryDelay * time.Duration(max(failures, 1))):
		}
	}
}

func watchOnce(ctx context.Context, c *caller, sessionID string, r *Renderer, o watchOptions, t *turn, cursor *string, result *watchResult) (bool, error) {
	req := &agentv1.WatchSessionRequest{SessionId: sessionID}
	if *cursor != "" {
		req.AfterCursor = cursor
	}
	// Close drains unread messages, so a live stream must be cancelled first or Close blocks.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.client.WatchSession(streamCtx, authed(c, req))
	if err != nil {
		return false, fmt.Errorf("watch session %s: %w", sessionID, err)
	}
	defer func() {
		cancel()
		_ = stream.Close()
	}()

	for stream.Receive() {
		msg := stream.Msg()
		view, err := r.Render(msg.GetSession(), msg.GetViewJson())
		if err != nil {
			return false, err
		}
		// Reading a whole reply can outlast the wait; a frame handled after it ended settles nothing.
		if ctx.Err() != nil {
			return false, nil
		}
		// The cursor moves only once the frame is fully handled, so a failed read below replays the frame.
		advance := func() {
			if msg.GetCursor() != "" {
				*cursor = msg.GetCursor()
			}
		}
		status := msg.GetSession().GetStatus()
		result.Status = status
		if !o.untilIdle {
			advance()
			if status == "archived" {
				return true, nil
			}
			continue
		}
		done, reply, failure := t.settled(status, view)
		if result.Joined = len(t.joined); t.tell(r) != nil {
			return false, t.tell(r)
		}
		for !done && t.doubt != "" {
			// The status and the view are written apart, so the reply may only be late. Look once more,
			// or for an input the view does not show, until the session has had time to pick it up.
			wait, final := replyGrace, true
			if t.doubt == failedUnseen {
				// The window opens once for the whole watch, so a reconnect does not start it over.
				if t.unseen.IsZero() {
					t.unseen = time.Now()
				}
				wait, final = pickupPoll, time.Since(t.unseen)+pickupPoll >= pickupGrace
			}
			select {
			case <-ctx.Done():
				return false, nil
			case <-time.After(wait):
			}
			again, err := c.client.GetSession(ctx, authed(c, &agentv1.GetSessionRequest{SessionId: sessionID}))
			if err != nil {
				return false, fmt.Errorf("get session %s: %w", sessionID, err)
			}
			if view, err = r.Render(again.Msg.GetSession(), again.Msg.GetViewJson()); err != nil {
				return false, err
			}
			if ctx.Err() != nil {
				return false, nil
			}
			result.Status = again.Msg.GetSession().GetStatus()
			if done, reply, failure = t.settled(result.Status, view); !done && t.doubt != "" && final {
				done, failure = true, t.doubt
			}
			if result.Joined = len(t.joined); t.tell(r) != nil {
				return false, t.tell(r)
			}
		}
		if done && failure == "" {
			// The wait ends here, so no later frame would read a reply this one could not.
			if reply = r.Recover(ctx, view, t.said, reply); ctx.Err() != nil {
				return false, nil
			}
		}
		advance()
		if done {
			result.Outcome, result.Reply, result.Error = outcomeSettled, reply, failure
			if failure != "" {
				result.Outcome = outcomeFailed
			}
			return true, nil
		}
	}
	if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("watch session %s: %w", sessionID, err)
	}
	return false, nil
}

// turn decides, frame by frame, whether the session has finished the work being waited for.
type turn struct {
	afterInput string
	// last is the newest message seen in the awaited input's turn. It places the turn in a view
	// that the turn has grown too long to keep the input's own message in.
	last    string
	lastSeq int64
	// base is the seq of the newest message from before the input was a message: the caller's, or else the
	// first frame's. A cut view holding only newer messages, without the input, has dropped the input's turn.
	base *int64
	// framed is set when base came from a frame, so the input may already have been out of the view by then.
	framed bool
	// doubt is why the turn counts as failed if a second look still shows what the last frame did:
	// the turn over without a reply at its end, as an interrupt leaves it, or gone from the view.
	doubt string
	// said is the id of the message the last settled gave as the reply.
	said string
	// joined are the later inputs that landed in the awaited turn; told is how many of them the watch has said.
	joined []string
	told   int
	// unseen is when the wait first found the input missing from an idle session's cut view.
	unseen time.Time
}

const (
	failedNoReply = "the turn ended without a reply"
	failedLost    = "the session moved past the input before its reply was seen"
	failedUnseen  = "the input is not in the session's view: its turn is too old to show, or the session never picked it up"
)

// mark takes the base from the first view that can give one. A frame without a view says nothing of its messages.
func (t *turn) mark(view *agentView) {
	switch {
	case t.base != nil || view == nil:
	case len(view.Messages) > 0:
		seq := view.Messages[len(view.Messages)-1].Seq
		t.base, t.framed = &seq, true
	case !view.Truncated:
		t.base = beforeFirst()
	}
}

// passed reports whether the view shows only messages newer than the base, so the input's own was cut away.
func (t *turn) passed(view *agentView) bool {
	return t.base != nil && view.Truncated && len(view.Messages) > 0 && view.Messages[0].Seq > *t.base
}

// settled reports whether the wait is over, with the agent's reply, or with why the turn counts as failed.
func (t *turn) settled(status string, view *agentView) (done bool, reply, failure string) {
	t.doubt, t.said = "", ""
	// A read made beside the stream can be ahead of frames the stream has yet to deliver; those say nothing new.
	if n := len(viewMessages(view)); t.last != "" && n > 0 && view.Messages[n-1].Seq < t.lastSeq {
		return false, "", ""
	}
	at, input := t.place(view)
	if t.afterInput != "" && !input {
		defer t.mark(view)
	}
	var doubt string
	if t.afterInput != "" && view != nil && at < 0 {
		var gone bool
		if gone, doubt = t.lost(status, view); gone {
			return true, "", failedLost
		}
	}
	// A reply the input already has stands, whatever happened to the session afterwards.
	if msg, failure := t.answered(view, at, input, status == "archived" || status == "failed"); msg != nil {
		t.said = msg.ID
		return replied(*msg)
	} else if failure != "" {
		return true, "", failure
	}
	switch status {
	case "failed":
		return true, "", "the session failed"
	case "archived":
		return true, "", "the session was archived"
	case "idle", "waiting_input":
	default:
		return false, "", ""
	}
	// A session with no view yet has not started on its first input.
	if view == nil {
		return false, "", ""
	}
	if view.Partial != "" || view.Retry != nil {
		return false, "", ""
	}
	for _, tool := range view.Tools {
		if tool.Status != "done" {
			return false, "", ""
		}
	}
	// The view shows nothing of the awaited turn, so the session is idle from another one.
	if t.afterInput != "" && at < 0 {
		t.doubt = doubt
		return false, "", ""
	}
	sent := -1
	if input {
		sent = at
	}
	// Queued inputs are work still to come, unless the awaited input already has its reply above them.
	if view.QueuedCount > 0 && sent < 0 {
		return false, "", ""
	}
	last := newest(view, len(view.Messages))
	if last < 0 || last <= sent || view.Messages[last].Role != "assistant" {
		if view.QueuedCount == 0 {
			t.doubt = failedNoReply
		}
		return false, "", ""
	}
	// A message that calls a tool is a step of the turn, not its reply, unless it is the error that ended it.
	if msg := view.Messages[last]; len(msg.ToolCalls) > 0 && !msg.IsError {
		if view.QueuedCount == 0 {
			t.doubt = failedNoReply
		}
		return false, "", ""
	}
	t.said = view.Messages[last].ID
	return replied(view.Messages[last])
}

func (t *turn) tell(r *Renderer) error {
	for ; t.told < len(t.joined); t.told++ {
		r.line("[a later message joined this turn; the reply will answer it too]")
	}
	if r.w.err != nil {
		return fmt.Errorf("write transcript: %w", r.w.err)
	}
	return nil
}

// lost is the one place that reads a view showing nothing of the awaited turn. gone: the turn was being followed
// and an input shows, so it is over with its reply cut away. Otherwise doubt is why the turn counts as failed if
// a second look at an idle session shows the same: a session still working may yet show the turn, or its end.
func (t *turn) lost(status string, view *agentView) (gone bool, doubt string) {
	followed := t.last != ""
	if followed {
		for _, msg := range view.Messages {
			if msg.outside() && !msg.joined() {
				return true, ""
			}
		}
	}
	switch {
	case status != "idle" && status != "waiting_input", view.QueuedCount > 0:
	case followed, t.passed(view):
		doubt = failedLost
	case view.Truncated && (t.base == nil || t.framed):
		doubt = failedUnseen
	}
	return false, doubt
}

// place finds the awaited turn in the view: the index of the input's own message, or once that has left
// a cut view, of the newest message already seen in its turn. It is -1 when the view shows neither.
func (t *turn) place(view *agentView) (at int, input bool) {
	at = -1
	if t.afterInput == "" || view == nil {
		return at, false
	}
	for i, msg := range view.Messages {
		if msg.InputID == t.afterInput {
			return i, true
		}
		if t.last != "" && msg.ID == t.last {
			at = i
		}
	}
	return at, false
}

// answered returns the reply to the awaited input once a later input follows it, or once the session is closed,
// so a session that has moved on to another turn does not hold the wait open or answer with that turn's reply.
// What the agent says after a later input answers that input, whether it steered the turn or started the next
// one, so it is never credited to this one; nor is anything in a view that no longer connects to the turn.
func (t *turn) answered(view *agentView, at int, input, closed bool) (reply *viewMessage, failure string) {
	if t.afterInput == "" || view == nil {
		return nil, ""
	}
	if at < 0 {
		return nil, ""
	}
	// The input's own message cannot be its reply; the newest message seen after it can.
	isReply := func(i int) bool {
		if i < 0 || (i <= at && input) {
			return false
		}
		return view.Messages[i].Role == "assistant" && len(view.Messages[i].ToolCalls) == 0
	}
	for i := at + 1; i < len(view.Messages); i++ {
		// A subagent's report joins the turn it was started in; only an input from outside ends that turn.
		if !view.Messages[i].outside() {
			continue
		}
		// Unless the API says it landed in the running turn, whose reply then answers it too (DEP-8781).
		if msg := view.Messages[i]; msg.joined() {
			if !slices.Contains(t.joined, msg.ID) {
				t.joined = append(t.joined, msg.ID)
			}
			continue
		}
		if said := newest(view, i); isReply(said) {
			return &view.Messages[said], ""
		}
		return nil, "another input followed before the agent replied"
	}
	last := len(view.Messages) - 1
	t.last, t.lastSeq = view.Messages[last].ID, view.Messages[last].Seq
	if said := newest(view, len(view.Messages)); closed && isReply(said) {
		return &view.Messages[said], ""
	}
	return nil, ""
}

// newest is the index of the last message before end that is not a subagent's report, which the agent may leave
// unanswered when it comes in after the reply. It is -1 when there is none.
func newest(view *agentView, end int) int {
	for i := end - 1; i >= 0; i-- {
		if msg := view.Messages[i]; msg.Role != "user" || msg.outside() {
			return i
		}
	}
	return -1
}

func replied(msg viewMessage) (done bool, reply, failure string) {
	switch {
	case msg.IsError:
		return true, "", "the agent's reply was an error: " + truncate(oneLine(msg.Text))
	case strings.TrimSpace(msg.Text) == "":
		return true, "", "the agent's reply was empty"
	}
	return true, msg.Text, ""
}

// wholeMessage reads the full text of a message the view cut short. Past the API's own limit the text is still cut,
// and ends in the same note the view puts there.
func wholeMessage(ctx context.Context, c *caller, sessionID, messageID string) (string, error) {
	for failures := 1; ; failures++ {
		// Each try has its own time, so one that hangs leaves the others theirs.
		try, cancel := context.WithTimeout(ctx, wholeMessageTimeout)
		resp, err := c.client.GetSessionMessage(try, authed(c, &agentv1.GetSessionMessageRequest{SessionId: sessionID, MessageId: messageID}))
		cancel()
		if err == nil {
			text := resp.Msg.GetText()
			if cut := resp.Msg.GetTruncatedBytes(); cut > 0 {
				text += fmt.Sprintf("\n[%d bytes truncated]", cut)
			}
			return text, nil
		}
		if !retryable(err) || failures >= wholeMessageTries {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", err
		case <-time.After(retryDelay):
		}
	}
}

// retryable reports whether err is a transient RPC failure.
// Any other error, such as a view that fails to decode, would fail the same way again.
func retryable(err error) bool {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return false
	}
	switch connectErr.Code() {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeUnknown, connect.CodeInternal:
		return true
	case connect.CodeInvalidArgument:
		// A connection that ends under an open stream, as with a GOAWAY, is reported by the client as a message cut short.
		return !connect.IsWireError(err) && strings.Contains(connectErr.Message(), "incomplete envelope")
	}
	return false
}

func viewMessages(view *agentView) []viewMessage {
	if view == nil {
		return nil
	}
	return view.Messages
}
