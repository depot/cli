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
    {"id": "2", "role": "assistant", "text": "Looking at it.\n",
      "toolCalls": [{"callId": "t1", "name": "bash", "summary": "go test ./pkg/foo/... -run TestX"}]}
  ],
  "tools": [{"callId": "t1", "name": "bash", "summary": "go test ./pkg/foo/... -run TestX", "status": "running", "output": "=== RUN TestX"}],
  "queued": [{"id": "q1", "mode": "steer", "text": "use go 1.25"}],
  "status": "running"
}`

const viewTwo = `{
  "messages": [
    {"id": "1", "role": "user", "text": "fix the test\nin pkg/foo"},
    {"id": "2", "role": "assistant", "text": "Looking at it.\n",
      "toolCalls": [{"callId": "t1", "name": "bash", "summary": "go test ./pkg/foo/... -run TestX"}]},
    {"id": "3", "role": "tool", "callId": "t1", "toolName": "bash", "summary": "go test ./pkg/foo/... -run TestX", "isError": true,
      "text": "=== RUN TestX\nFAIL TestX\n"},
    {"id": "4", "role": "user", "text": "use go 1.25"},
    {"id": "5", "role": "assistant", "text": "", "toolCalls": [{"callId": "t2", "name": "edit", "summary": "pkg/foo/foo_test.go"}]},
    {"id": "6", "role": "tool", "callId": "t2", "toolName": "edit", "summary": "pkg/foo/foo_test.go", "text": "applied"},
    {"id": "7", "role": "assistant", "isError": true, "text": "interrupted\nby user"}
  ],
  "tools": [{"callId": "t2", "name": "edit", "summary": "pkg/foo/foo_test.go", "status": "done"}],
  "queued": [],
  "status": "idle"
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
		"[running]",
		"> fix the test",
		"> in pkg/foo",
		"Looking at it.",
		"→ bash go test ./pkg/foo/... -run TestX",
		"(queued steer: use go 1.25)",
		"  ← bash error: FAIL TestX (+1 lines)",
		"> use go 1.25",
		"→ edit pkg/foo/foo_test.go",
		"  ← edit ok: applied",
		"(error: interrupted by user)",
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
		// Once another line cuts into a partial, it stops streaming and the final message prints whole.
		`{"messages": [], "partial": "Again", "tools": [], "queued": [], "status": "running"}`,
		fmt.Sprintf(`{"messages": [], "partial": "Again and", "retry": {"at": %d, "error": "overloaded"}, "tools": [], "queued": [{"id": "q", "mode": "followup", "text": "then this"}], "status": "running"}`, retryAt.UnixMilli()),
		`{"messages": [], "partial": "Again and again", "tools": [], "queued": [], "status": "running"}`,
		`{"messages": [{"id": "3", "role": "assistant", "text": "Again and again."}], "tools": [], "queued": [], "status": "running"}`,
	} {
		if err := r.RenderView(view); err != nil {
			t.Fatalf("RenderView: %v", err)
		}
	}
	want := strings.Join([]string{
		"Hello world.",
		"Nex",
		"(error: aborted)",
		"Again and",
		"(queued follow-up: then this)",
		"(retrying at 12:00:00: overloaded)",
		"Again and again.",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("rendered output mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRendererStreamsAcrossTheRunningStatus(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	idle := &agentv1.DepotAgentSession{Status: "idle"}
	running := &agentv1.DepotAgentSession{Status: "running"}
	for _, resp := range []*agentv1.WatchSessionResponse{
		{Session: idle},
		{Session: running, ViewJson: `{"messages": [], "partial": "Hel"}`},
		{Session: running, ViewJson: `{"messages": [], "partial": "Hello"}`},
		{Session: running, ViewJson: `{"messages": [{"id": "1", "role": "assistant", "text": "Hello."}]}`},
	} {
		if err := r.Render(resp); err != nil {
			t.Fatalf("Render: %v", err)
		}
	}
	if got, want := out.String(), "[idle]\n[running]\nHello.\n"; got != want {
		t.Fatalf("rendered output mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRendererCountsQueuedInputsOutsideTheView(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	view := `{"messages": [], "queued": [{"id": "q1", "mode": "followup", "text": "one"}], "queuedCount": 3}`
	for range 2 {
		if err := r.RenderView(view); err != nil {
			t.Fatalf("RenderView: %v", err)
		}
	}
	if got := out.String(); strings.Count(got, "(2 more queued)") != 1 {
		t.Fatalf("expected one omission line, got:\n%s", got)
	}
}

func TestRendererNamesACallThatScrolledOut(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	view := `{"messages": [{"id": "9", "role": "tool", "callId": "t9", "toolName": "read", "summary": "go.mod", "text": "module x"}],
		"tools": [], "queued": [], "status": "running", "truncated": true}`
	if err := r.RenderView(view); err != nil {
		t.Fatalf("RenderView: %v", err)
	}
	want := "(older messages omitted)\n→ read go.mod\n  ← read ok: module x\n"
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
