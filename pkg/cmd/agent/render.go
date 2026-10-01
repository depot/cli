package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
)

const maxSummaryRunes = 120

// conversationView is the subset of the harness's ConversationView projection
// that the CLI renders.
// Entries are immutable once committed,
// so an entry ID that has been printed never needs printing again.
type conversationView struct {
	Entries []viewEntry `json:"entries"`
}

type viewEntry struct {
	ID    string        `json:"id"`
	Kind  string        `json:"kind"`
	Model []viewMessage `json:"model"`
}

type viewMessage struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	ToolName     string          `json:"toolName"`
	IsError      bool            `json:"isError"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
}

type contentPart struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Renderer prints each newly committed transcript entry
// and each session status change exactly once.
type Renderer struct {
	w          io.Writer
	seen       map[string]bool
	lastStatus string
}

func NewRenderer(w io.Writer) *Renderer {
	return &Renderer{w: w, seen: map[string]bool{}}
}

// Render prints the frame's new entries before its status,
// so a settled status reads after the turn that produced it.
func (r *Renderer) Render(resp *agentv1.WatchSessionResponse) error {
	if err := r.RenderView(resp.GetViewJson()); err != nil {
		return err
	}
	if s := resp.GetSession(); s != nil && s.Status != r.lastStatus {
		r.lastStatus = s.Status
		fmt.Fprintf(r.w, "[%s]\n", s.Status)
	}
	return nil
}

func (r *Renderer) RenderView(viewJSON string) error {
	if viewJSON == "" {
		return nil
	}
	var view conversationView
	if err := json.Unmarshal([]byte(viewJSON), &view); err != nil {
		return fmt.Errorf("decode session view: %w", err)
	}
	for _, entry := range view.Entries {
		if entry.ID == "" || r.seen[entry.ID] {
			continue
		}
		r.seen[entry.ID] = true
		for _, msg := range entry.Model {
			r.renderMessage(msg)
		}
	}
	return nil
}

func (r *Renderer) renderMessage(msg viewMessage) {
	switch msg.Role {
	case "user":
		text := strings.TrimSpace(contentText(msg.Content))
		for _, line := range strings.Split(text, "\n") {
			fmt.Fprintf(r.w, "> %s\n", line)
		}
	case "assistant":
		for _, part := range contentParts(msg.Content) {
			switch part.Type {
			case "text":
				if text := strings.TrimSpace(part.Text); text != "" {
					fmt.Fprintln(r.w, text)
				}
			case "toolCall":
				fmt.Fprintf(r.w, "→ %s %s\n", part.Name, summarizeArgs(part.Arguments))
			}
		}
		if msg.StopReason == "error" || msg.StopReason == "aborted" {
			reason := msg.StopReason
			if msg.ErrorMessage != "" {
				reason += ": " + msg.ErrorMessage
			}
			fmt.Fprintf(r.w, "(%s)\n", truncate(oneLine(reason)))
		}
	case "toolResult":
		outcome := "ok"
		if msg.IsError {
			outcome = "error"
		}
		text := strings.TrimSpace(contentText(msg.Content))
		lines := strings.Split(text, "\n")
		summary := truncate(lines[0])
		if len(lines) > 1 {
			summary += fmt.Sprintf(" (+%d lines)", len(lines)-1)
		}
		fmt.Fprintf(r.w, "  ← %s %s: %s\n", msg.ToolName, outcome, summary)
	}
}

// contentParts accepts both content shapes pi-ai uses:
// a bare string or an array of typed parts.
func contentParts(raw json.RawMessage) []contentPart {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []contentPart{{Type: "text", Text: s}}
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	return parts
}

func contentText(raw json.RawMessage) string {
	var texts []string
	for _, part := range contentParts(raw) {
		if part.Type == "text" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// summarizeArgs prefers the argument a human scans for
// (a command or a path) over the full JSON object.
func summarizeArgs(raw json.RawMessage) string {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err == nil {
		for _, key := range []string{"command", "path", "file_path", "pattern", "url"} {
			if v, ok := args[key].(string); ok && v != "" {
				return truncate(oneLine(v))
			}
		}
	}
	return truncate(oneLine(string(raw)))
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
