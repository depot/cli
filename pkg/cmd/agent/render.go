package agent

import (
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
	ID        string         `json:"id"`
	InputID   string         `json:"inputId"`
	Role      string         `json:"role"`
	Text      string         `json:"text"`
	CallID    string         `json:"callId"`
	ToolName  string         `json:"toolName"`
	Summary   string         `json:"summary"`
	IsError   bool           `json:"isError"`
	ToolCalls []viewToolCall `json:"toolCalls"`
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
	w          io.Writer
	seen       map[string]bool
	lastStatus string
	// streamed is the prefix of the partial response already printed,
	// and open means the cursor is still on its line.
	// broken means another line cut into it, so it cannot be continued.
	streamed string
	open     bool
	broken   bool
	// from is the input the transcript starts at; everything older is skipped until it shows up.
	from string
}

func NewRenderer(w io.Writer) *Renderer {
	return &Renderer{w: safeWriter{w}, seen: map[string]bool{}}
}

// safeWriter replaces control and format characters other than newline and tab, so text from a session
// cannot send escape sequences to the terminal or reorder what it shows with bidi overrides.
type safeWriter struct{ w io.Writer }

func (s safeWriter) Write(b []byte) (int, error) {
	if _, err := io.WriteString(s.w, safeText(string(b))); err != nil {
		return 0, err
	}
	return len(b), nil
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
	status := session.GetStatus()
	if status == "running" {
		r.status(status)
	}
	view, err := r.renderView(viewJSON)
	if err != nil {
		return nil, err
	}
	if session != nil {
		r.status(status)
	}
	return view, nil
}

func (r *Renderer) status(status string) {
	if status != r.lastStatus {
		r.lastStatus = status
		r.line("[%s]", status)
	}
}

func (r *Renderer) renderView(viewJSON string) (*agentView, error) {
	if viewJSON == "" {
		return nil, nil
	}
	var view agentView
	if err := json.Unmarshal([]byte(viewJSON), &view); err != nil {
		return nil, fmt.Errorf("decode session view: %w", err)
	}
	if view.Truncated && r.once("truncated") {
		r.line("(older messages omitted)")
	}
	r.skipOlder(&view)
	if r.from != "" {
		// Everything in this view belongs to turns before the awaited input.
		return &view, nil
	}
	for _, msg := range view.Messages {
		if msg.ID != "" && r.once("message:"+msg.ID) {
			r.renderMessage(msg)
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
	return &view, nil
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
