package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// did is what the command did to the session before watching it, said when the watch then fails.
	did string
}

type watchResult struct {
	SessionID string `json:"sessionId"`
	InputID   string `json:"inputId,omitempty"`
	Status    string `json:"status"`
	Outcome   string `json:"outcome"`
	Reply     string `json:"reply,omitempty"`
	Error     string `json:"error,omitempty"`
	URL       string `json:"url,omitempty"`
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
	t := &turn{}
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
		var done bool
		var reply, failure string
		if o.afterInput != "" {
			// A frame that only streams more of a reply cannot change what became of the input.
			if at := status + ":" + newestID(view); at != t.asked {
				if done, reply, failure, err = awaited(ctx, c, sessionID, o.afterInput, status, view, r); err != nil {
					return false, err
				}
				if ctx.Err() != nil {
					return false, nil
				}
				t.asked = at
			}
		} else {
			done, reply, failure = t.settled(status, view)
		}
		for !done && t.doubt != "" {
			// The status and the view are written apart, so the reply may only be late. Look once more.
			select {
			case <-ctx.Done():
				return false, nil
			case <-time.After(replyGrace):
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
			if done, reply, failure = t.settled(result.Status, view); !done && t.doubt != "" {
				done, failure = true, t.doubt
			}
		}
		if done && failure == "" && o.afterInput == "" {
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

// awaited asks the API what became of the input being waited for. The API reads its answer off what the session
// stored, so nothing here is worked out from the messages around it.
func awaited(ctx context.Context, c *caller, sessionID, inputID, status string, view *agentView, r *Renderer) (done bool, reply, failure string, err error) {
	resp, err := c.client.GetSessionInput(ctx, authed(c, &agentv1.GetSessionInputRequest{SessionId: sessionID, InputId: inputID}))
	if err != nil {
		return false, "", "", fmt.Errorf("get input %s: %w", inputID, err)
	}
	switch resp.Msg.GetOutcome() {
	case "answered":
		id := resp.Msg.GetAnswerMessageId()
		// A reply the frame holds whole is already in the transcript and needs no read.
		for _, msg := range viewMessages(view) {
			if msg.ID == id && msg.TruncatedBytes == 0 {
				done, reply, failure = replied(msg)
				return done, reply, failure, nil
			}
		}
		text, err := r.Reply(id)
		if err != nil {
			return false, "", "", fmt.Errorf("get message %s: %w", id, err)
		}
		if strings.TrimSpace(text) == "" {
			return true, "", "the agent's reply was empty", nil
		}
		return true, text, "", nil
	case "unanswered":
		switch resp.Msg.GetReason() {
		case "aborted":
			return true, "", "the turn was stopped before the agent replied", nil
		case "model_error":
			return true, "", "the agent's reply was an error: " + truncate(oneLine(resp.Msg.GetDetail())), nil
		}
		return true, "", "the turn ended without a reply: " + resp.Msg.GetReason(), nil
	case "withdrawn":
		return true, "", "the input was cancelled before the agent took it", nil
	}
	switch status {
	case "failed":
		return true, "", "the session failed", nil
	case "archived":
		return true, "", "the session was archived", nil
	}
	return false, "", "", nil
}

// turn decides, frame by frame, whether the session has finished its work.
type turn struct {
	// doubt is why the session counts as failed if a second look still shows what the last frame did:
	// idle without a reply at its end, as an interrupt leaves it.
	doubt string
	// said is the id of the message the last settled gave as the reply.
	said string
	// asked is the status and newest message of the last frame the API was asked about the awaited input for.
	asked string
}

func newestID(view *agentView) string {
	if n := len(viewMessages(view)); n > 0 {
		return view.Messages[n-1].ID
	}
	return ""
}

const failedNoReply = "the turn ended without a reply"

// settled reports whether the wait is over, with the agent's reply, or with why the session counts as failed.
func (t *turn) settled(status string, view *agentView) (done bool, reply, failure string) {
	t.doubt, t.said = "", ""
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
	// Queued inputs are work still to come.
	if view.QueuedCount > 0 {
		return false, "", ""
	}
	last := newest(view, len(view.Messages))
	// A message that calls a tool is a step of the turn, not its reply, unless it is the error that ended it.
	if last < 0 || view.Messages[last].Role != "assistant" || (len(view.Messages[last].ToolCalls) > 0 && !view.Messages[last].IsError) {
		t.doubt = failedNoReply
		return false, "", ""
	}
	t.said = view.Messages[last].ID
	return replied(view.Messages[last])
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
