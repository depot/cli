package agent

import (
	"bytes"
	"strings"
	"testing"

	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
)

const viewOne = `{
  "conversation": {"id": "conv_1"},
  "entries": [
    {"id": "e1", "kind": "pi.head"},
    {"id": "e2", "kind": "pi.user", "model": [{"role": "user", "content": "fix the test\nin pkg/foo", "timestamp": 1}]},
    {"id": "e3", "kind": "pi.assistant", "model": [{"role": "assistant", "content": [
      {"type": "thinking", "thinking": "hidden"},
      {"type": "text", "text": "Looking at it."},
      {"type": "toolCall", "id": "t1", "name": "bash", "arguments": {"command": "go test ./pkg/foo/...\n  -run TestX"}}
    ], "stopReason": "toolUse"}]}
  ],
  "docs": {}
}`

const viewTwo = `{
  "entries": [
    {"id": "e1", "kind": "pi.head"},
    {"id": "e2", "kind": "pi.user", "model": [{"role": "user", "content": "fix the test\nin pkg/foo"}]},
    {"id": "e3", "kind": "pi.assistant", "model": [{"role": "assistant", "content": [{"type": "text", "text": "Looking at it."}]}]},
    {"id": "e4", "kind": "pi.toolResult", "model": [{"role": "toolResult", "toolCallId": "t1", "toolName": "bash", "isError": true,
      "content": [{"type": "text", "text": "FAIL TestX\nline 2\nline 3"}]}]},
    {"id": "e5", "kind": "pi.assistant", "model": [{"role": "assistant", "content": [
      {"type": "toolCall", "id": "t2", "name": "edit", "arguments": {"path": "pkg/foo/foo_test.go", "oldText": "a", "newText": "b"}},
      {"type": "toolCall", "id": "t3", "name": "custom", "arguments": {"n": 1}}
    ]}]},
    {"id": "e6", "kind": "pi.assistant", "model": [{"role": "assistant", "content": [], "stopReason": "aborted", "errorMessage": "interrupted\nby user"}]}
  ]
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
		"→ bash go test ./pkg/foo/... -run TestX",
		"[running]",
		"  ← bash error: FAIL TestX (+2 lines)",
		"→ edit pkg/foo/foo_test.go",
		`→ custom {"n": 1}`,
		"(aborted: interrupted by user)",
		"[idle]",
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
