package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/charmbracelet/colorprofile"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"golang.org/x/term"
)

const maxSummaryRunes = 120

// agentView is the bounded projection the harness pushes as view_json.
// Its source of truth is the runtime's DepotAgentView.
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
// It is safe to use from several goroutines.
type Renderer struct {
	mu         sync.Mutex
	w          io.Writer
	seen       map[string]bool
	lastStatus string
	// streamed is the prefix of the partial response already printed,
	// and open means the cursor is still on its line.
	// broken means another line cut into it, so it cannot be continued.
	streamed string
	open     bool
	broken   bool
	// sawMessage means a view held a message, so the session's first turn has started.
	sawMessage bool

	// styled takes plugin view lines, which sanitise their own text before styling it.
	styled   io.Writer
	width    func() int
	viewMode string
	viewRevs map[string]int64
}

const (
	viewsFull    = "full"
	viewsCompact = "compact"
	viewsNone    = "none"
)

func NewRenderer(w io.Writer) *Renderer {
	return &Renderer{
		w:        safeWriter{w},
		seen:     map[string]bool{},
		styled:   colorprofile.NewWriter(w, os.Environ()),
		width:    func() int { return terminalWidth(w) },
		viewMode: viewsFull,
		viewRevs: map[string]int64{},
	}
}

// SetViewMode picks how plugin views print: full, compact, or none.
func (r *Renderer) SetViewMode(mode string) {
	r.viewMode = mode
}

func validateViewMode(mode string) error {
	switch mode {
	case viewsFull, viewsCompact, viewsNone:
		return nil
	}
	return fmt.Errorf("unsupported --views %q (valid: full, compact, none)", mode)
}

func terminalWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width > 0 {
			return width
		}
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 80
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

// safeError sanitizes an error's text, since server messages reach the terminal through it.
type safeError struct{ err error }

func (e safeError) Error() string { return safeText(e.err.Error()) }
func (e safeError) Unwrap() error { return e.err }

func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			return '_'
		}
		return r
	}, s)
}

// Render prints a running status before the frame's view, so it cannot cut into the streamed partial,
// and any other status after it, so a settled status reads after the turn that produced it.
func (r *Renderer) Render(resp *agentv1.WatchSessionResponse) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := resp.GetSession().GetStatus()
	if status == "running" {
		r.status(status)
	}
	if err := r.renderView(resp.GetViewJson()); err != nil {
		return err
	}
	r.renderPluginViews(resp.GetViewsJson(), resp.GetSession().GetSessionId())
	if resp.GetSession() != nil {
		r.status(status)
	}
	return nil
}

func (r *Renderer) status(status string) {
	if status != r.lastStatus {
		r.lastStatus = status
		r.line("[%s]", status)
	}
}

func (r *Renderer) RenderView(viewJSON string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.renderView(viewJSON)
}

// Notices returns a writer for whole lines from outside the session, such as command errors,
// which end a streaming line before printing instead of cutting into it.
func (r *Renderer) Notices() io.Writer {
	return noticeWriter{r}
}

type noticeWriter struct{ r *Renderer }

func (n noticeWriter) Write(b []byte) (int, error) {
	n.r.mu.Lock()
	defer n.r.mu.Unlock()
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		n.r.line("%s", l)
	}
	return len(b), nil
}

func (r *Renderer) renderView(viewJSON string) error {
	if viewJSON == "" {
		return nil
	}
	var view agentView
	if err := json.Unmarshal([]byte(viewJSON), &view); err != nil {
		return fmt.Errorf("decode session view: %w", err)
	}
	if view.Truncated && r.once("truncated") {
		r.line("(older messages omitted)")
	}
	if len(view.Messages) > 0 {
		r.sawMessage = true
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
	return nil
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

// renderPluginViews prints each plugin view whose rev changed,
// waiting while a partial response is still streaming so a view never cuts into it;
// a partial another line already cut into prints whole at the end, so it does not hold views back.
func (r *Renderer) renderPluginViews(viewsJSON, sessionID string) {
	if r.viewMode == viewsNone {
		return
	}
	if r.streamed != "" && !r.broken {
		return
	}
	views, err := parsePluginViews(viewsJSON)
	if err != nil {
		if r.once("views-error") {
			r.line("(plugin views not shown: %v)", err)
		}
		return
	}
	for _, v := range views.Views {
		if rev, ok := r.viewRevs[v.ref()]; ok && rev == v.Rev {
			continue
		}
		r.viewRevs[v.ref()] = v.Rev
		lines, chips := renderView(v, r.width(), r.viewMode == viewsCompact)
		for _, l := range lines {
			r.styledLine(l)
		}
		if len(chips) > 0 && r.once("view-hint:"+v.ref()) {
			hint := fmt.Sprintf("(run a control: depot agent session action %s %s <key>)", sessionID, v.ref())
			r.styledLine(renderLine(viewLine{span(hint, mutedStyle)}, r.width()))
		}
	}
}

func (r *Renderer) styledLine(s string) {
	if r.open {
		fmt.Fprintln(r.w)
		r.open = false
	}
	fmt.Fprintln(r.styled, s)
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

// settled reports whether a session has stopped working
// and is waiting on the user or has failed.
func settled(status string) bool {
	switch status {
	case "idle", "waiting_input", "failed", "archived":
		return true
	}
	return false
}

// terminal reports whether a session has stopped and will not run again on its own.
func terminal(status string) bool {
	return status == "failed" || status == "archived"
}
