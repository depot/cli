package agent

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
)

const viewOne = `{
  "messages": [
    {"id": "1", "role": "user", "text": "fix the test\nin pkg/foo"},
    {"id": "2", "role": "assistant", "text": "Looking at it.\n",
      "toolCalls": [{"callId": "t1", "name": "bash", "summary": "go test ./pkg/foo/... -run TestX"}]}
  ],
  "tools": [{"callId": "t1", "name": "bash", "summary": "go test ./pkg/foo/... -run TestX", "status": "running", "output": "=== RUN TestX"}],
  "queued": [{"id": "q1", "mode": "steer", "text": "use go 1.25"}],
  "queuedCount": 3
}`

const viewTwo = `{
  "messages": [
    {"id": "1", "role": "user", "text": "fix the test\nin pkg/foo"},
    {"id": "2", "role": "assistant", "text": "Looking at it.\n",
      "toolCalls": [{"callId": "t1", "name": "bash", "summary": "go test ./pkg/foo/... -run TestX"}]},
    {"id": "3", "role": "tool", "callId": "t1", "toolName": "bash", "summary": "go test ./pkg/foo/... -run TestX", "isError": true,
      "text": "=== RUN TestX\nFAIL TestX\n"},
    {"id": "4", "role": "user", "inputId": "dai_2", "text": "use go 1.25"},
    {"id": "5", "role": "assistant", "text": "", "toolCalls": [{"callId": "t2", "name": "edit", "summary": "pkg/foo/foo_test.go"}]},
    {"id": "6", "role": "tool", "callId": "t2", "toolName": "edit", "summary": "pkg/foo/foo_test.go", "text": "applied"},
    {"id": "7", "role": "assistant", "isError": true, "text": "interrupted\nby user"}
  ],
  "tools": [{"callId": "t2", "name": "edit", "summary": "pkg/foo/foo_test.go", "status": "done"}],
  "queued": []
}`

func render(t *testing.T, r *Renderer, status string, views ...string) {
	t.Helper()
	for _, view := range views {
		var session *agentv1.DepotAgentSession
		if status != "" {
			session = &agentv1.DepotAgentSession{Status: status}
		}
		if _, err := r.Render(session, view); err != nil {
			t.Fatalf("Render: %v", err)
		}
	}
}

func TestRendererPrintsEachEntryOnce(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	render(t, r, "running", viewOne, viewOne, viewTwo)
	render(t, r, "idle", viewTwo)

	want := strings.Join([]string{
		"[running]",
		"> fix the test",
		"> in pkg/foo",
		"Looking at it.",
		"→ bash go test ./pkg/foo/... -run TestX",
		"(queued steer: use go 1.25)",
		"(2 more queued)",
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

func TestRendererStartsAtAnInput(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out).From("dai_2")
	render(t, r, "", viewOne, viewTwo)
	want := "(queued steer: use go 1.25)\n(2 more queued)\n> use go 1.25\n→ edit pkg/foo/foo_test.go\n  ← edit ok: applied\n(error: interrupted by user)\n"
	if got := out.String(); got != want {
		t.Fatalf("rendered output mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRendererStreamsPartial(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	retryAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	render(t, r, "",
		`{"messages": [], "partial": "Hel"}`,
		`{"messages": [], "partial": "Hello wor"}`,
		`{"messages": [{"id": "1", "role": "assistant", "text": "Hello world."}]}`,
		// An aborted response ends the streamed line before its error.
		`{"messages": [{"id": "1", "role": "assistant", "text": "Hello world."}], "partial": "Nex"}`,
		`{"messages": [{"id": "2", "role": "assistant", "isError": true, "text": "aborted"}]}`,
		// Once another line cuts into a partial, it stops streaming and the final message prints whole.
		`{"messages": [], "partial": "Again"}`,
		fmt.Sprintf(`{"messages": [], "partial": "Again and", "retry": {"at": %d, "error": "overloaded"}}`, retryAt.UnixMilli()),
		`{"messages": [], "partial": "Again and again"}`,
		`{"messages": [{"id": "3", "role": "assistant", "text": "Again and again."}]}`,
	)
	want := strings.Join([]string{
		"Hello world.",
		"Nex",
		"(error: aborted)",
		"Again and",
		"(retrying at 12:00:00: overloaded)",
		"Again and again.",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("rendered output mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRendererNeutralizesTerminalEscapes(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	render(t, r, "", `{"messages": [{"id": "1", "role": "assistant", "text": "a\u001b[2Jb\u202ec"}]}`, "")
	if got := out.String(); got != "a_[2Jb_c\n" {
		t.Fatalf("got %q", got)
	}
	if _, err := r.Render(nil, "{"); err == nil {
		t.Fatal("an invalid view rendered without an error")
	}
}
