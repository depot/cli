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

	defaultWatchTimeout = 30 * time.Minute
	// The command's exit code when the wait ran out of time; a failed turn exits 1.
	timeoutExitCode = 2
)

// How long an idle session whose turn has no reply is given to show one before the turn counts as failed.
var replyGrace = 10 * time.Second

type watchOptions struct {
	// afterInput waits for the reply to this input instead of for the session to be idle at all.
	afterInput string
	untilIdle  bool
	timeout    time.Duration
	// url is the session's URL when the API gave one; otherwise it is built from the organization.
	url string
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

Runs until interrupted, or with --until-idle until the session has finished its work. With --until-idle the exit code
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
			return watchAndReport(cmd, c, f.output, args[0], o)
		},
	}
	f.add(cmd)
	cmd.Flags().BoolVar(&o.untilIdle, "until-idle", false, "Exit once the session has finished its work")
	cmd.Flags().StringVar(&o.afterInput, "after-input", "", "Exit once this input (dai_...) has its reply; implies --until-idle")
	cmd.Flags().DurationVar(&o.timeout, "timeout", defaultWatchTimeout, "How long --until-idle waits")
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
		return err
	}
	if !o.untilIdle {
		return nil
	}
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
	if o.untilIdle && o.timeout > 0 {
		var cancel context.CancelFunc
		deadline, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}
	t := &turn{afterInput: o.afterInput}
	cursor := ""
	for {
		done, err := watchOnce(deadline, c, sessionID, r, o, t, &cursor, &result)
		if done {
			return result, nil
		}
		if ctx.Err() != nil {
			return result, nil
		}
		if deadline.Err() != nil {
			result.Outcome = outcomeTimeout
			return result, nil
		}
		if err != nil && !retryable(err) {
			return result, err
		}
		select {
		case <-deadline.Done():
		case <-time.After(time.Second):
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
		if msg.GetCursor() != "" {
			*cursor = msg.GetCursor()
		}
		status := msg.GetSession().GetStatus()
		result.Status = status
		if !o.untilIdle {
			if status == "archived" {
				return true, nil
			}
			continue
		}
		done, reply, failure := t.settled(status, view)
		if !done && t.unanswered {
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
			result.Status = again.Msg.GetSession().GetStatus()
			if done, reply, failure = t.settled(result.Status, view); !done && t.unanswered {
				done, failure = true, "the turn ended without a reply"
			}
		}
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
	// ran means the session was seen running with the input in view, which stands in for the input
	// once a long turn has pushed its message out of the bounded view.
	ran bool
	// unanswered means the last frame showed the turn over with something other than a reply at its end,
	// as an interrupted turn leaves it.
	unanswered bool
}

// settled reports whether the wait is over, with the agent's reply, or with why the turn counts as failed.
func (t *turn) settled(status string, view *agentView) (done bool, reply, failure string) {
	t.unanswered = false
	// A reply the input already has stands, whatever happened to the session afterwards.
	if msg := t.answered(view, status == "archived"); msg != nil {
		return replied(*msg)
	}
	switch status {
	case "failed":
		return true, "", "the session failed"
	case "archived":
		return true, "", "the session was archived"
	case "running":
		t.ran = t.ran || t.afterInput == "" || t.shows(view)
		return false, "", ""
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
	sent := -1
	if t.afterInput != "" {
		for i, msg := range view.Messages {
			if msg.InputID == t.afterInput {
				sent = i
			}
		}
		if sent < 0 && !(t.ran && view.Truncated) {
			return false, "", ""
		}
	}
	// Queued inputs are work still to come, unless the awaited input already has its reply above them.
	if view.QueuedCount > 0 && sent < 0 {
		return false, "", ""
	}
	last := len(view.Messages) - 1
	if last < 0 || last <= sent || view.Messages[last].Role != "assistant" {
		t.unanswered = view.QueuedCount == 0
		return false, "", ""
	}
	return replied(view.Messages[last])
}

// answered returns the reply to the awaited input once a later input follows it, or once the session is closed,
// so a session that has moved on to another turn does not hold the wait open or answer with that turn's reply.
func (t *turn) answered(view *agentView, closed bool) *viewMessage {
	if t.afterInput == "" || view == nil {
		return nil
	}
	sent := -1
	for i, msg := range view.Messages {
		switch {
		case msg.InputID == t.afterInput:
			sent = i
		case sent >= 0 && msg.Role == "user":
			// A message that steered the turn follows a tool call or nothing, not a reply.
			if prev := view.Messages[i-1]; i-1 > sent && prev.Role == "assistant" && len(prev.ToolCalls) == 0 {
				return &prev
			}
			return nil
		}
	}
	if last := len(view.Messages) - 1; closed && sent >= 0 && last > sent && view.Messages[last].Role == "assistant" && len(view.Messages[last].ToolCalls) == 0 {
		return &view.Messages[last]
	}
	return nil
}

func (t *turn) shows(view *agentView) bool {
	if view == nil {
		return false
	}
	for _, msg := range view.Messages {
		if msg.InputID == t.afterInput {
			return true
		}
	}
	return false
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

// retryable reports whether err is a transient RPC failure.
// Any other error, such as a view that fails to decode, would fail the same way again.
func retryable(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeUnknown, connect.CodeInternal:
		var connectErr *connect.Error
		return errors.As(err, &connectErr)
	}
	return false
}
