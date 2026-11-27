package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
)

const maxSummaryRunes = 120

// agentView is the bounded projection of a session that the API returns as view_json.
type agentView struct {
	Messages []viewMessage `json:"messages"`
	Partial  string        `json:"partial"`
	Retry    *viewRetry    `json:"retry"`
	Tools    []viewTool    `json:"tools"`
	Queued   []viewQueued  `json:"queued"`
	// QueuedCount is the true number of queued inputs; Queued holds at most the first few.
	QueuedCount int  `json:"queuedCount"`
	Truncated   bool `json:"truncated"`
}

type viewMessage struct {
	ID string `json:"id"`
	// Seq orders messages across the whole session; a later message has a higher one.
	Seq       int64          `json:"seq"`
	InputID   string         `json:"inputId"`
	Sender    viewSender     `json:"sender"`
	Role      string         `json:"role"`
	Text      string         `json:"text"`
	CallID    string         `json:"callId"`
	ToolName  string         `json:"toolName"`
	Summary   string         `json:"summary"`
	IsError   bool           `json:"isError"`
	ToolCalls []viewToolCall `json:"toolCalls"`
	// TruncatedBytes is how much of the message the view left out of Text, which then ends in a note saying so.
	TruncatedBytes int64 `json:"truncatedBytes"`
}

// outside reports whether a person or another system sent the message, rather than the session's own subagent.
func (m viewMessage) outside() bool {
	return m.Role == "user" && m.Sender.Kind != "subagent"
}

// viewSender says who wrote a user message; the kind "subagent" is the session's own subagent reporting back.
type viewSender struct {
	Kind string `json:"kind"`
}

type viewToolCall struct {
	CallID  string `json:"callId"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

type viewRetry struct {
	At    int64  `json:"at"`
	Error string `json:"error"`
}

type viewTool struct {
	CallID  string `json:"callId"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
}

type viewQueued struct {
	ID   string `json:"id"`
	Mode string `json:"mode"`
	Text string `json:"text"`
}

// Renderer turns successive views into an append-only transcript.
// Messages, tool calls, queued inputs, and retries print once by id;
// the partial response streams as it grows.
type Renderer struct {
	w          *safeWriter
	seen       map[string]bool
	lastStatus string
	newest     int64
	// streamed is the prefix of the partial response already printed,
	// and open means the cursor is still on its line.
	// broken means another line cut into it, so it cannot be continued.
	streamed string
	open     bool
	broken   bool
	// from is the input the transcript starts at; everything older is skipped until it shows up.
	from string
	// whole reads a message's full text by id, and full keeps each one read while the view still holds it.
	// unread counts the frames left to read again a message whose read failed in a way that may pass.
	whole  func(messageID string) (string, error)
	full   map[string]string
	unread map[string]int
}

func NewRenderer(w io.Writer) *Renderer {
	return &Renderer{w: &safeWriter{w: w}, seen: map[string]bool{}}
}

// safeWriter replaces control and format characters other than newline and tab, so text from a session
// cannot send escape sequences to the terminal or reorder what it shows with bidi overrides.
// It keeps the first write error and writes nothing after it, so Render can report a closed pipe.
type safeWriter struct {
	w   io.Writer
	err error
}

func (s *safeWriter) Write(b []byte) (int, error) {
	if s.err == nil {
		_, s.err = io.WriteString(s.w, safeText(string(b)))
	}
	return len(b), s.err
}

func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			return '_'
		}
		return r
	}, s)
}

// Render prints what a frame adds and returns its view, nil when the frame carries none.
// A running status prints before the view, so it cannot cut into the streamed partial,
// and any other status after it, so a settled status reads after the turn that produced it.
func (r *Renderer) Render(session *agentv1.DepotAgentSession, viewJSON string) (*agentView, error) {
	view, err := decodeView(viewJSON)
	if err != nil {
		return nil, err
	}
	for i := range viewMessages(view) {
		if text, ok := r.full[view.Messages[i].ID]; ok {
			view.Messages[i].Text = text
		}
	}
	// A snapshot older than one already printed, as a stream frame behind a direct read is, has nothing to add.
	if n := len(viewMessages(view)); n > 0 {
		if view.Messages[n-1].Seq < r.newest {
			return view, nil
		}
		r.newest = view.Messages[n-1].Seq
		r.keep(view)
	}
	status := session.GetStatus()
	if status == "running" {
		r.status(status)
	}
	if view != nil {
		r.renderView(view)
	}
	if session != nil {
		r.status(status)
	}
	if r.w.err != nil {
		return nil, fmt.Errorf("write transcript: %w", r.w.err)
	}
	return view, nil
}

func (r *Renderer) status(status string) {
	if status != r.lastStatus {
		r.lastStatus = status
		r.line("[%s]", status)
	}
}

func decodeView(viewJSON string) (*agentView, error) {
	if viewJSON == "" {
		return nil, nil
	}
	var view agentView
	if err := json.Unmarshal([]byte(viewJSON), &view); err != nil {
		return nil, fmt.Errorf("decode session view: %w", err)
	}
	return &view, nil
}

func (r *Renderer) renderView(view *agentView) {
	if view.Truncated && r.once("truncated") {
		r.line("(older messages omitted)")
	}
	r.skipOlder(view)
	if r.from != "" {
		// Everything in this view belongs to turns before the awaited input.
		return
	}
	for i := range view.Messages {
		if msg := &view.Messages[i]; msg.ID != "" && r.once("message:"+msg.ID) {
			err := r.fill(msg)
			r.renderMessage(*msg)
			if err != nil {
				r.line("(the rest of this reply could not be read: %s)", truncate(oneLine(err.Error())))
				if retryable(err) {
					r.unread[msg.ID] = wholeMessageTries
				}
			}
		}
	}
	for _, tool := range view.Tools {
		r.call(tool.CallID, tool.Name, tool.Summary)
	}
	r.streamPartial(view.Partial)
	for _, q := range view.Queued {
		if r.once("queued:" + q.ID) {
			r.line("(queued %s: %s)", queuedMode(q.Mode), truncate(oneLine(q.Text)))
		}
	}
	if more := view.QueuedCount - len(view.Queued); more > 0 && r.once(fmt.Sprintf("queued-more:%d", view.QueuedCount)) {
		r.line("(%d more queued)", more)
	}
	if view.Retry != nil && r.once(fmt.Sprintf("retry:%d", view.Retry.At)) {
		at := time.UnixMilli(view.Retry.At).Format(time.TimeOnly)
		r.line("(retrying at %s: %s)", at, truncate(oneLine(view.Retry.Error)))
	}
}

// Whole makes the renderer read the full text of a reply the view cut short, for the transcript and for the
// view Render returns. A reply is an assistant message that calls no tool; nothing else is read.
func (r *Renderer) Whole(read func(messageID string) (string, error)) *Renderer {
	r.whole, r.full, r.unread = read, map[string]string{}, map[string]int{}
	return r
}

// keep forgets the messages the view no longer holds, so a long watch does not keep every reply it read,
// and reads again a reply that an earlier frame could not.
func (r *Renderer) keep(view *agentView) {
	held := make(map[string]bool, len(view.Messages))
	for i := range view.Messages {
		msg := &view.Messages[i]
		held[msg.ID] = true
		if r.unread[msg.ID] == 0 {
			continue
		}
		if r.unread[msg.ID]--; r.fill(msg) == nil {
			delete(r.unread, msg.ID)
			// Printed as plain lines: a response streaming now is another turn's, and keeps its place.
			r.line("(the whole reply, read on a later try)")
			r.line("%s", strings.TrimSpace(msg.Text))
		}
	}
	for id := range r.full {
		if !held[id] {
			delete(r.full, id)
		}
	}
	for id, left := range r.unread {
		if !held[id] || left <= 0 {
			delete(r.unread, id)
		}
	}
}

// Recover reads again the reply with this id if a failed read left it cut, with the tries it has left, and returns its text.
func (r *Renderer) Recover(ctx context.Context, view *agentView, id, reply string) string {
	for i := range viewMessages(view) {
		msg := &view.Messages[i]
		if msg.ID != id {
			continue
		}
		for r.unread[id] > 0 && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(retryDelay):
				r.keep(view)
			}
		}
		return msg.Text
	}
	return reply
}

func (r *Renderer) fill(msg *viewMessage) error {
	if r.whole == nil || msg.TruncatedBytes <= 0 || msg.Role != "assistant" || msg.IsError || len(msg.ToolCalls) > 0 {
		return nil
	}
	text, err := r.whole(msg.ID)
	if err != nil {
		return err
	}
	r.full[msg.ID], msg.Text = text, text
	return nil
}

// Reply returns the whole text of the message with this id, read by the id unless a frame already read it, and
// prints it when the transcript has not shown it whole: its turn outgrew the view, or its read failed.
func (r *Renderer) Reply(id string) (string, error) {
	text, held := r.full[id]
	if !held {
		var err error
		if text, err = r.whole(id); err != nil {
			return "", err
		}
	}
	switch {
	case r.once("message:" + id):
		r.finishPartial(text)
	case r.unread[id] > 0:
		delete(r.unread, id)
		r.line("(the whole reply, read on a later try)")
		r.line("%s", strings.TrimSpace(text))
	}
	return text, nil
}

// From starts the transcript at the message of this input, leaving out the session's earlier turns.
func (r *Renderer) From(inputID string) *Renderer {
	r.from = inputID
	return r
}

func (r *Renderer) skipOlder(view *agentView) {
	if r.from == "" {
		return
	}
	older := view.Messages
	for i, msg := range view.Messages {
		if msg.InputID == r.from {
			older, r.from = view.Messages[:i], ""
			break
		}
	}
	// Until the input itself shows up, nothing proves a message belongs to its turn: an input that has left
	// the queue may not be in the view yet, and what is running may still be the turn before it.
	for _, msg := range older {
		r.seen["message:"+msg.ID] = true
		r.seen["call:"+msg.CallID] = true
		for _, call := range msg.ToolCalls {
			r.seen["call:"+call.CallID] = true
		}
	}
}

func (r *Renderer) renderMessage(msg viewMessage) {
	switch msg.Role {
	case "user":
		for _, line := range strings.Split(strings.TrimSpace(msg.Text), "\n") {
			r.line("> %s", line)
		}
	case "assistant":
		if msg.IsError {
			r.line("(error: %s)", truncate(oneLine(msg.Text)))
		} else {
			r.finishPartial(msg.Text)
		}
		for _, call := range msg.ToolCalls {
			r.call(call.CallID, call.Name, call.Summary)
		}
	case "tool":
		// A result whose call scrolled out of the view still names it first.
		r.call(msg.CallID, msg.ToolName, msg.Summary)
		outcome := "ok"
		if msg.IsError {
			outcome = "error"
		}
		r.line("  ← %s %s: %s", msg.ToolName, outcome, toolSummary(msg.Text))
	}
}

// call prints a tool call once by call id,
// whether its assistant message, live slot, or result shows it first.
func (r *Renderer) call(callID, name, summary string) {
	if callID != "" && !r.once("call:"+callID) {
		return
	}
	if summary == "" {
		r.line("→ %s", name)
		return
	}
	r.line("→ %s %s", name, summary)
}

// streamPartial prints whatever the partial response added since the last frame;
// each partial is a prefix of the next and of the final message.
// Once another line cuts into it, streaming stops
// and the final message prints whole.
func (r *Renderer) streamPartial(partial string) {
	if partial == "" {
		// No response is in flight, so the next partial starts fresh, on its own line.
		r.streamed = ""
		r.broken = false
		if r.open {
			fmt.Fprintln(r.w)
			r.open = false
		}
		return
	}
	if r.broken || !strings.HasPrefix(partial, r.streamed) {
		return
	}
	if suffix := partial[len(r.streamed):]; suffix != "" {
		fmt.Fprint(r.w, suffix)
		r.streamed = partial
		r.open = !strings.HasSuffix(partial, "\n")
	}
}

// finishPartial prints a completed assistant message,
// or only its remainder when the partial already streamed its start.
func (r *Renderer) finishPartial(text string) {
	streamed, broken := r.streamed, r.broken
	r.streamed, r.broken = "", false
	if streamed != "" && !broken && strings.HasPrefix(text, streamed) {
		rest := strings.TrimRight(text[len(streamed):], " \t\n")
		fmt.Fprint(r.w, rest)
		if r.open || rest != "" {
			fmt.Fprintln(r.w)
		}
		r.open = false
		return
	}
	if text = strings.TrimSpace(text); text != "" {
		r.line("%s", text)
	}
}

// line prints one line, first ending a partial response left mid-line.
func (r *Renderer) line(format string, args ...any) {
	if r.streamed != "" {
		r.broken = true
	}
	if r.open {
		fmt.Fprintln(r.w)
		r.open = false
	}
	fmt.Fprintf(r.w, format+"\n", args...)
}

func (r *Renderer) once(key string) bool {
	if r.seen[key] {
		return false
	}
	r.seen[key] = true
	return true
}

func queuedMode(mode string) string {
	if mode == "followup" {
		return "follow-up"
	}
	return mode
}

// toolSummary shows the last line of a tool result,
// since the projection keeps a long result's tail.
func toolSummary(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "(no output)"
	}
	summary := truncate(oneLine(lines[len(lines)-1]))
	if len(lines) > 1 {
		summary += fmt.Sprintf(" (+%d lines)", len(lines)-1)
	}
	return summary
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string) string {
	runes := []rune(s)
	if len(runes) <= maxSummaryRunes {
		return s
	}
	return string(runes[:maxSummaryRunes-1]) + "…"
}
