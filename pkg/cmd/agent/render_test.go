package agent

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
)

// viewOne and viewTwo follow the runtime's DepotAgentView projection.
const viewOne = `{
  "messages": [
    {"id": "1", "role": "user", "text": "fix the test\nin pkg/foo"},
    {"id": "2", "role": "assistant", "text": "Looking at it.\n"}
  ],
  "tools": [{"callId": "t1", "name": "bash", "status": "running", "output": "=== RUN TestX"}],
  "queued": [{"id": "q1", "mode": "steer", "text": "use go 1.25"}],
  "status": "running"
}`

const viewTwo = `{
  "messages": [
    {"id": "1", "role": "user", "text": "fix the test\nin pkg/foo"},
    {"id": "2", "role": "assistant", "text": "Looking at it.\n"},
    {"id": "3", "role": "tool", "toolName": "bash", "isError": true, "text": "=== RUN TestX\nFAIL TestX\n"},
    {"id": "4", "role": "user", "text": "use go 1.25"},
    {"id": "5.0", "role": "assistant", "isError": true, "text": "interrupted\nby user"}
  ],
  "tools": [
    {"callId": "t1", "name": "bash", "status": "done"},
    {"callId": "t2", "name": "edit", "status": "running"},
    {"callId": "t3", "name": "read", "status": "done"}
  ],
  "queued": [],
  "status": "idle",
  "truncated": true
}`

func TestRendererPrintsEachEntryOnce(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)

	running := &agentv1.DepotAgentSession{Status: "running"}
	idle := &agentv1.DepotAgentSession{Status: "idle"}
	for _, resp := range []*agentv1.WatchSessionResponse{
		{Session: running, ViewJson: viewOne},
		{Session: running, ViewJson: viewOne},
		{Session: running, ViewJson: viewTwo},
		{Session: idle, ViewJson: viewTwo},
	} {
		if err := r.Render(resp); err != nil {
			t.Fatalf("Render: %v", err)
		}
	}

	want := strings.Join([]string{
		"> fix the test",
		"> in pkg/foo",
		"Looking at it.",
		"→ bash",
		"(queued steer: use go 1.25)",
		"[running]",
		"(older messages omitted)",
		"  ← bash error: FAIL TestX (+1 lines)",
		"> use go 1.25",
		"(error: interrupted by user)",
		"→ edit",
		"[idle]",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("rendered output mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRendererStreamsPartial(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	retryAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	for _, view := range []string{
		`{"messages": [], "partial": "Hel", "tools": [], "queued": [], "status": "running"}`,
		`{"messages": [], "partial": "Hello wor", "tools": [], "queued": [], "status": "running"}`,
		`{"messages": [{"id": "1", "role": "assistant", "text": "Hello world."}], "tools": [], "queued": [], "status": "running"}`,
		// An aborted response ends the streamed line before its error.
		`{"messages": [{"id": "1", "role": "assistant", "text": "Hello world."}], "partial": "Nex", "tools": [], "queued": [], "status": "running"}`,
		`{"messages": [{"id": "2", "role": "assistant", "isError": true, "text": "aborted"}], "tools": [], "queued": [], "status": "running"}`,
		// A partial that no longer extends what printed stops streaming; the final message prints whole.
		`{"messages": [], "partial": "Again", "tools": [], "queued": [], "status": "running"}`,
		fmt.Sprintf(`{"messages": [], "partial": "…tail only", "retry": {"at": %d, "error": "overloaded"}, "tools": [], "queued": [{"id": "q", "mode": "followUp", "text": "then this"}], "status": "running"}`, retryAt.UnixMilli()),
		`{"messages": [{"id": "3", "role": "assistant", "text": "Again, in full"}], "tools": [], "queued": [], "status": "running"}`,
	} {
		if err := r.RenderView(view); err != nil {
			t.Fatalf("RenderView: %v", err)
		}
	}
	want := strings.Join([]string{
		"Hello world.",
		"Nex",
		"(error: aborted)",
		"Again",
		"(queued follow-up: then this)",
		"(retrying at 12:00:00: overloaded)",
		"Again, in full",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("rendered output mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRendererEmptyAndInvalidView(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	if err := r.RenderView(""); err != nil {
		t.Fatalf("empty view: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("empty view printed %q", out.String())
	}
	if err := r.RenderView("{not json"); err == nil {
		t.Fatal("expected an error for an invalid view")
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("é", maxSummaryRunes+5)
	got := []rune(truncate(long))
	if len(got) != maxSummaryRunes || got[len(got)-1] != '…' {
		t.Fatalf("truncate gave %d runes ending %q", len(got), got[len(got)-1])
	}
	if truncate("short") != "short" {
		t.Fatal("truncate altered a short string")
	}
}

func TestParseAttachLine(t *testing.T) {
	tests := []struct {
		line string
		want attachCommand
	}{
		{"", attachCommand{kind: attachNone}},
		{"   ", attachCommand{kind: attachNone}},
		{"  do the thing  ", attachCommand{kind: attachInput, mode: modeFollowup, content: "do the thing"}},
		{"/steer  use go 1.25", attachCommand{kind: attachInput, mode: modeSteer, content: "use go 1.25"}},
		{"/steer", attachCommand{kind: attachNone}},
		{"/interrupt", attachCommand{kind: attachInterrupt}},
		{"/quit", attachCommand{kind: attachQuit}},
		{"/nope x", attachCommand{kind: attachUnknown, content: "/nope"}},
	}
	for _, tt := range tests {
		if got := parseAttachLine(tt.line); got != tt.want {
			t.Errorf("parseAttachLine(%q) = %+v, want %+v", tt.line, got, tt.want)
		}
	}
}

func TestParseModel(t *testing.T) {
	m := parseModel("bedrock/anthropic.claude-opus-5-5")
	if m.Provider != "bedrock" || m.ModelId != "anthropic.claude-opus-5-5" {
		t.Fatalf("parseModel with provider = %+v", m)
	}
	m = parseModel("claude-opus-5-5")
	if m.Provider != "" || m.ModelId != "claude-opus-5-5" {
		t.Fatalf("parseModel without provider = %+v", m)
	}
}
