package agent

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
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

func TestRendererStartsANewLineForAPartialAfterOneWasCleared(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	for _, view := range []string{`{"partial": "first"}`, `{"partial": ""}`, `{"partial": "second"}`} {
		if err := r.RenderView(view); err != nil {
			t.Fatalf("RenderView: %v", err)
		}
	}
	if got := out.String(); got != "first\nsecond" {
		t.Fatalf("got %q, want the second partial on its own line", got)
	}
}

func TestRendererNoticeEndsTheStreamingLine(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	if err := r.RenderView(`{"partial": "thinking"}`); err != nil {
		t.Fatalf("RenderView: %v", err)
	}
	fmt.Fprintf(r.Notices(), "(send failed: %v)\n", "boom")
	if err := r.RenderView(`{"messages": [{"id": "m1", "role": "assistant", "text": "thinking done"}]}`); err != nil {
		t.Fatalf("RenderView: %v", err)
	}
	if got, want := out.String(), "thinking\n(send failed: boom)\nthinking done\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
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

func TestRendererNamesSendersAndAttachments(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	view := `{
		"messages": [
			{"id": "m1", "role": "user", "text": "first line\nsecond", "sender": {"kind": "user", "name": "ada"},
			 "attachments": [{"name": "trace.log", "sizeBytes": 2048}]},
			{"id": "m2", "role": "user", "text": "from chat", "sender": {"kind": "slack", "name": "grace"}},
			{"id": "m3", "role": "user", "text": "no sender"},
			{"id": "m4", "role": "user", "text": "report", "sender": {"kind": "subagent", "name": "tests‮"}}
		],
		"queued": [{"id": "q1", "mode": "followup", "text": "later", "sender": {"kind": "user", "name": "ada"},
			"attachments": [{"name": "a.png", "sizeBytes": 1}, {"name": "b.png", "sizeBytes": 1}]}]
	}`
	if err := r.RenderView(view); err != nil {
		t.Fatalf("RenderView: %v", err)
	}
	want := strings.Join([]string{
		"> ada: first line",
		"> second",
		"  [file trace.log, 2.048kB]",
		"> grace (slack): from chat",
		"> no sender",
		"> tests_ (subagent): report",
		"(queued follow-up from ada: later [2 files])",
	}, "\n") + "\n"
	if got := out.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
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

func TestRendererNeutralizesTerminalEscapes(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	for _, view := range []string{
		`{"partial": "ok \u001b]52;c;aGk=\u0007"}`,
		`{"messages": [{"id": "1", "role": "assistant", "text": "ok \u001b]52;c;aGk=\u0007 done\u009b2J"}]}`,
	} {
		if err := r.RenderView(view); err != nil {
			t.Fatalf("RenderView: %v", err)
		}
	}
	if got, want := out.String(), "ok _]52;c;aGk=_ done_2J\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSessionTableNeutralizesTerminalEscapes(t *testing.T) {
	var out bytes.Buffer
	sessions := []*agentv1.DepotAgentSession{{SessionId: "s1", Status: "idle", Title: "t\u001b[2J", RepoUrl: ptr("https://x\u0007\tmore")}}
	if err := writeSessionTable(&out, sessions); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\u001b\u0007") || !strings.Contains(out.String(), "https://x_ more") {
		t.Fatalf("the table should hold no control characters:\n%q", out.String())
	}
}

func TestSafeTextDropsBidiAndZeroWidthCharacters(t *testing.T) {
	if got, want := safeText("a‮txt.exe⁦b⁩​c\td\n"), "a_txt.exe_b__c\td\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCommandErrorsAreSanitized(t *testing.T) {
	cause := errors.New("server said \u001b]52;c;aGk=\u0007 ‮ok")
	root := &cobra.Command{Use: "root"}
	root.AddCommand(&cobra.Command{Use: "fail", RunE: func(*cobra.Command, []string) error {
		return fmt.Errorf("watch session s1: %w", cause)
	}})
	sanitizeErrors(root)
	root.SetArgs([]string{"fail"})
	root.SilenceErrors, root.SilenceUsage = true, true
	err := root.Execute()
	if got, want := err.Error(), "watch session s1: server said _]52;c;aGk=_ _ok"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Fatal("sanitizing should keep the wrapped error")
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
