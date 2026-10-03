package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"connectrpc.com/connect"
	"github.com/charmbracelet/x/ansi"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
)

// todosViews is the todos example from the DEP-7248 design, as the api sends it to the CLI.
const todosViews = `{"rev": 7, "views": [
  {"plugin": "todos", "id": "plan", "rev": 3, "v": 1, "title": "Plan", "blocks": [
    {"type": "status", "label": "2 of 4 done", "tone": "info", "progress": 0.5},
    {"type": "list", "items": [
      {"id": "t1", "text": "Write migration", "checked": true},
      {"id": "t3", "text": "Ship PR", "accessory": {"type": "button", "label": "Done", "action": "complete", "value": "t3"}},
      {"id": "t4", "text": "Tell #eng", "accessory": {"type": "select", "action": "set_owner", "placeholder": "Owner",
        "options": [{"label": "agent", "value": "agent"}, {"label": "me", "value": "me"}]}}]},
    {"type": "form", "id": "add", "inputs": [{"name": "text", "label": "New item", "kind": "text", "maxLength": 200}],
     "submit": {"type": "button", "label": "Add", "action": "add"}},
    {"type": "actions", "elements": [{"type": "button", "label": "Clear done", "action": "clear", "style": "danger",
      "confirm": {"title": "Clear done items?", "text": "Removes 2 items."}}]}],
   "kinds": {"cli": {"keys": [{"key": "a", "action": "add"}], "compact": "2/4 done"}}}
]}`

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain drops the CLI's own SGR styling, leaving anything else a plugin might have smuggled through.
func plain(lines []string) string {
	return sgr.ReplaceAllString(strings.Join(lines, "\n"), "")
}

func todosView(t *testing.T) pluginView {
	t.Helper()
	vs, err := parsePluginViews(todosViews)
	if err != nil {
		t.Fatal(err)
	}
	return vs.Views[0]
}

func TestRenderTodosView(t *testing.T) {
	lines, chips := renderView(todosView(t), 80, false)
	want := strings.Join([]string{
		"── todos: Plan ──",
		"● 2 of 4 done  ▕█████·····▏ 50%",
		"[x] Write migration",
		"• Ship PR  [1] Done (t3)",
		"• Tell #eng  [2] Owner ▾ (agent|me)",
		"[a] Add (New item)",
		"[3] Clear done",
	}, "\n")
	if got := plain(lines); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	var keys []string
	for _, c := range chips {
		keys = append(keys, c.Key+"="+c.Action+"/"+c.Kind)
	}
	if got := strings.Join(keys, " "); got != "1=complete/button 2=set_owner/select a=add/form 3=clear/button" {
		t.Fatalf("chips: %s", got)
	}

	compact, _ := renderView(todosView(t), 80, true)
	if got := plain(compact); got != "── todos: Plan ── 2/4 done" {
		t.Fatalf("compact: %q", got)
	}
}

func TestRenderViewBlocks(t *testing.T) {
	v := pluginView{Plugin: "p", ID: "v", Title: "T"}
	if err := json.Unmarshal([]byte(`[
	  {"type": "section", "title": "Build", "text": "two\nlines", "blocks": [{"type": "text", "text": "nested"}]},
	  {"type": "section", "title": "Hidden", "collapsed": true, "blocks": [{"type": "text", "text": "secret"}]},
	  {"type": "fields", "items": [{"label": "Branch", "value": "main"}]},
	  {"type": "list", "ordered": true, "items": [{"text": "one"}, {"text": "two"}]},
	  {"type": "table", "columns": [{"label": "Name"}, {"label": "Count", "align": "right"}], "rows": [["alpha", "1"], ["b", "22"]]},
	  {"type": "markdown", "text": "see [docs](https://depot.dev/docs) or [bad](javascript:alert(1))"},
	  {"type": "link", "text": "Run", "url": "https://depot.dev/r/1"},
	  {"type": "link", "text": "Plain", "url": "http://example.com"},
	  {"type": "image", "src": "data:image/png;base64,AAAA", "alt": "chart"},
	  {"type": "divider"},
	  {"type": "carousel", "alt": "3 slides"},
	  {"type": "carousel"}
	]`), &v.Blocks); err != nil {
		t.Fatal(err)
	}
	lines, _ := renderView(v, 24, false)
	want := strings.Join([]string{
		"── p: T ──",
		"Build",
		"two",
		"lines",
		"  nested",
		"▸ Hidden",
		"Branch: main",
		"1. one",
		"2. two",
		"Name   Count",
		"alpha      1",
		"b         22",
		"see docs (https://depot…",
		"Run (https://depot.dev/…",
		"Plain",
		"[image: chart]",
		strings.Repeat("─", 24),
		"3 slides",
		"1 item not shown",
	}, "\n")
	if got := plain(lines); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 24 {
			t.Errorf("line wider than the terminal (%d): %q", w, l)
		}
	}
}

func TestRenderViewCapsLines(t *testing.T) {
	var items []string
	for i := range 50 {
		items = append(items, fmt.Sprintf(`{"text": "item %d"}`, i))
	}
	vs, err := parsePluginViews(`{"views": [{"plugin": "p", "id": "v", "title": "Long", "blocks": [{"type": "list", "items": [` + strings.Join(items, ",") + `]}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	lines, _ := renderView(vs.Views[0], 80, false)
	if len(lines) != 1+viewLineCap+1 {
		t.Fatalf("expected rule + %d lines + footer, got %d", viewLineCap, len(lines))
	}
	if got := plain(lines[len(lines)-1:]); got != "… 10 more lines" {
		t.Fatalf("footer %q", got)
	}
}

func TestRenderViewKeysFallBackToNumbers(t *testing.T) {
	vs, err := parsePluginViews(`{"views": [{"plugin": "p", "id": "v", "title": "K", "blocks": [
	  {"type": "actions", "elements": [
	    {"type": "button", "label": "A", "action": "a1"},
	    {"type": "button", "label": "B", "action": "b1"},
	    {"type": "button", "label": "C", "action": "c1"}]}],
	  "kinds": {"cli": {"keys": [{"key": "7", "action": "a1"}, {"key": "x", "action": "b1"}, {"key": "x", "action": "c1"}, {"key": "z", "action": "extra", "value": "v"}]}}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	lines, chips := renderView(vs.Views[0], 80, false)
	if got := plain(lines); got != "── p: K ──\n[1] A  [x] B  [2] C\n[z] extra (v)" {
		t.Fatalf("got:\n%s", got)
	}
	if len(chips) != 4 || chips[3].Kind != "key" || *chips[3].Value != "v" {
		t.Fatalf("chips %+v", chips)
	}
}

// hostile carries every kind of escape a plugin might try: CSI, OSC 52 clipboard and OSC 8 hyperlinks,
// bidi overrides and isolates, and zero-width characters.
const hostile = "\x1b[2J\x1b]52;c;cGF5bG9hZA==\x07\x1b]8;;https://evil.example\x1b\\\u202ecod.exe\u2066\u200b\u200d\ufeff\u009b31m"

func TestRenderViewNeutralisesHostileStrings(t *testing.T) {
	h, _ := json.Marshal(hostile)
	hs := string(h)
	doc := `{"views": [{"plugin": ` + hs + `, "id": "v", "title": ` + hs + `, "blocks": [
	  {"type": "section", "title": ` + hs + `, "text": ` + hs + `, "accessory": {"type": "button", "label": ` + hs + `, "action": "go", "value": ` + hs + `}},
	  {"type": "text", "text": ` + hs + `, "tone": "danger"},
	  {"type": "markdown", "text": "[` + strings.Trim(hs, `"`) + `](https://depot.dev/` + strings.Trim(hs, `"`) + `)"},
	  {"type": "fields", "items": [{"label": ` + hs + `, "value": ` + hs + `}]},
	  {"type": "list", "items": [{"text": ` + hs + `, "checked": false}]},
	  {"type": "table", "columns": [{"label": ` + hs + `}], "rows": [[` + hs + `]]},
	  {"type": "status", "label": ` + hs + `, "tone": "warning", "detail": ` + hs + `},
	  {"type": "image", "src": "data:image/png;base64,AA", "alt": ` + hs + `},
	  {"type": "link", "text": ` + hs + `, "url": "https://depot.dev/` + strings.Trim(hs, `"`) + `"},
	  {"type": "link", "text": "ok", "url": ` + hs + `},
	  {"type": "actions", "elements": [{"type": "select", "action": "s", "placeholder": ` + hs + `, "options": [{"label": ` + hs + `, "value": ` + hs + `}]}]},
	  {"type": "form", "id": "f", "inputs": [{"name": "n", "label": ` + hs + `, "kind": "text"}], "submit": {"type": "button", "label": ` + hs + `, "action": "f"}},
	  {"type": "mystery", "alt": ` + hs + `}],
	  "kinds": {"cli": {"keys": [{"key": ` + hs + `, "action": "go"}], "compact": ` + hs + `}}}]}`
	vs, err := parsePluginViews(doc)
	if err != nil {
		t.Fatal(err)
	}
	full, chips := renderView(vs.Views[0], 200, false)
	compact, _ := renderView(vs.Views[0], 200, true)
	for name, out := range map[string]string{"full": plain(full), "compact": plain(compact)} {
		for _, r := range out {
			if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
				t.Fatalf("%s view kept %U:\n%q", name, r, out)
			}
		}
		if !strings.Contains(out, "cod.exe") {
			t.Fatalf("%s view lost the visible text:\n%s", name, out)
		}
	}
	if strings.Contains(plain(full), " (https://depot.dev/") {
		t.Fatalf("a link with control characters printed as a live target:\n%s", plain(full))
	}
	for _, c := range chips {
		if _, err := strconv.Atoi(c.Key); err != nil && !validViewKey(c.Key) {
			t.Fatalf("chip key %q was not sanitised", c.Key)
		}
	}
}

func TestRendererPrintsViewsOnRevChange(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	r.width = func() int { return 80 }
	bumped := strings.Replace(todosViews, `"rev": 3`, `"rev": 4`, 1)
	streaming := `{"messages": [], "partial": "Working"}`
	done := `{"messages": [{"id": "m1", "role": "assistant", "text": "Working on it"}]}`
	for _, resp := range []*agentv1.WatchSessionResponse{
		{Session: &agentv1.DepotAgentSession{SessionId: "s1", Status: "idle"}, ViewsJson: todosViews},
		{Session: &agentv1.DepotAgentSession{SessionId: "s1", Status: "idle"}, ViewsJson: todosViews},
		// A rev change mid-stream waits for the partial to finish.
		{Session: &agentv1.DepotAgentSession{SessionId: "s1", Status: "running"}, ViewJson: streaming, ViewsJson: bumped},
		{Session: &agentv1.DepotAgentSession{SessionId: "s1", Status: "idle"}, ViewJson: done, ViewsJson: bumped},
	} {
		if err := r.Render(resp); err != nil {
			t.Fatal(err)
		}
	}
	got := out.String()
	if n := strings.Count(got, "── todos: Plan ──"); n != 2 {
		t.Fatalf("expected the view twice (rev 3, rev 4), got %d:\n%s", n, got)
	}
	if strings.Count(got, "session action s1 todos/plan <key>") != 1 {
		t.Fatalf("expected one action hint:\n%s", got)
	}
	if !strings.Contains(got, "[running]\nWorking on it\n── todos: Plan ──") {
		t.Fatalf("the second view cut into the streamed response:\n%s", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("styling reached a non-terminal writer:\n%q", got)
	}
}

func TestRendererPrintsViewsOnceAnotherLineCutsTheStream(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	r.width = func() int { return 80 }
	streaming := &agentv1.WatchSessionResponse{
		Session: &agentv1.DepotAgentSession{SessionId: "s1", Status: "running"}, ViewJson: `{"partial": "Working"}`, ViewsJson: todosViews,
	}
	if err := r.Render(streaming); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "── todos: Plan ──") {
		t.Fatalf("the view cut into the live partial:\n%s", out.String())
	}
	fmt.Fprintln(r.Notices(), "(send failed: boom)")
	if err := r.Render(streaming); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(send failed: boom)\n── todos: Plan ──") {
		t.Fatalf("the view waited on a partial that can no longer continue:\n%s", out.String())
	}
}

func TestRendererReportsBadViewsOnce(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out)
	for range 2 {
		if err := r.Render(&agentv1.WatchSessionResponse{Session: &agentv1.DepotAgentSession{Status: "idle"}, ViewsJson: "{bad"}); err != nil {
			t.Fatalf("a bad views_json must not end the watch: %v", err)
		}
	}
	if strings.Count(out.String(), "plugin views not shown") != 1 {
		t.Fatalf("got:\n%s", out.String())
	}
}

type fakeViewService struct {
	fakeAgentService
	viewsJSON    string
	gets         []*agentv1.GetSessionRequest
	invokes      []*agentv1.InvokeViewActionRequest
	invokeFailed int
}

func (f *fakeViewService) GetSession(_ context.Context, req *connect.Request[agentv1.GetSessionRequest]) (*connect.Response[agentv1.GetSessionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, req.Msg)
	return connect.NewResponse(&agentv1.GetSessionResponse{Session: &agentv1.DepotAgentSession{SessionId: req.Msg.SessionId}, ViewsJson: f.viewsJSON}), nil
}

func (f *fakeViewService) InvokeViewAction(_ context.Context, req *connect.Request[agentv1.InvokeViewActionRequest]) (*connect.Response[agentv1.InvokeViewActionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invokes = append(f.invokes, req.Msg)
	if len(f.invokes) <= f.invokeFailed {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("try again"))
	}
	return connect.NewResponse(&agentv1.InvokeViewActionResponse{Input: &agentv1.DepotAgentInput{InputId: "in_9", Mode: "plugin_call"}}), nil
}

func TestInvokeViewAction(t *testing.T) {
	type tc struct {
		name        string
		req         actionRequest
		wantErr     string
		wantAction  string
		wantValue   string
		wantForm    string
	}
	cases := []tc{
		{name: "chip number", req: actionRequest{name: "1"}, wantAction: "complete", wantValue: "t3"},
		{name: "action name", req: actionRequest{name: "complete"}, wantAction: "complete", wantValue: "t3"},
		{name: "select by value", req: actionRequest{name: "2", value: ptr("me")}, wantAction: "set_owner", wantValue: "me"},
		{name: "select needs value", req: actionRequest{name: "set_owner"}, wantErr: "pass --value (options: agent, me)"},
		{name: "form by key", req: actionRequest{name: "a", fields: []string{"text=Write docs"}}, wantAction: "add", wantForm: `{"text":"Write docs"}`},
		{name: "field on a button", req: actionRequest{name: "1", fields: []string{"text=x"}}, wantErr: "only for a form"},
		{name: "danger button runs", req: actionRequest{name: "3"}, wantAction: "clear"},
		{name: "unknown control", req: actionRequest{name: "zap"}, wantErr: "no action or key"},
		{name: "unknown view", req: actionRequest{name: "1", viewID: "other"}, wantErr: "no view todos/other (views: todos/plan)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeViewService{viewsJSON: todosViews, invokeFailed: 1}
			s := startFake(t, f)
			req := c.req
			req.sessionID, req.plugin = "s1", "todos"
			if req.viewID == "" {
				req.viewID = "plan"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			resp, err := invokeViewAction(ctx, s, req)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("expected error containing %q, got %v", c.wantErr, err)
				}
				if len(f.invokes) != 0 {
					t.Fatalf("sent %d invokes despite the error", len(f.invokes))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resp.GetInput().GetInputId() != "in_9" {
				t.Fatalf("resp %v", resp)
			}
			if g := f.gets[0].GetClient(); g.GetName() != "cli" || g.GetBlocks() != 1 || strings.Join(g.GetViewKinds(), ",") != "cli" {
				t.Fatalf("GetSession sent client %v", g)
			}
			if len(f.invokes) != 2 || f.invokes[0].GetClientRequestId() == "" || f.invokes[0].GetClientRequestId() != f.invokes[1].GetClientRequestId() {
				t.Fatalf("expected one retry with one client_request_id, got %v", f.invokes)
			}
			got := f.invokes[1]
			if got.SessionId != "s1" || got.Plugin != "todos" || got.ViewId != "plan" || got.Action != c.wantAction ||
				got.GetValue() != c.wantValue || got.GetFormJson() != c.wantForm {
				t.Fatalf("invoke = %v", got)
			}
		})
	}
}

func TestWatchAdvertisesTheCLIViewKind(t *testing.T) {
	f := &clientRecorder{fakeAgentService: fakeAgentService{streams: [][]*agentv1.WatchSessionResponse{{frame("failed", "")}}}}
	s := startFake(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := watchSession(ctx, s, "s1", NewRenderer(&bytes.Buffer{}), untilSettled); err != nil {
		t.Fatal(err)
	}
	if c := f.client; c.GetName() != "cli" || c.GetBlocks() != 1 || strings.Join(c.GetViewKinds(), ",") != "cli" {
		t.Fatalf("WatchSession sent client %v", c)
	}
}

type clientRecorder struct {
	fakeAgentService
	once   sync.Once
	client *agentv1.ViewClient
}

func (f *clientRecorder) WatchSession(ctx context.Context, req *connect.Request[agentv1.WatchSessionRequest], stream *connect.ServerStream[agentv1.WatchSessionResponse]) error {
	f.once.Do(func() { f.client = req.Msg.GetClient() })
	return f.fakeAgentService.WatchSession(ctx, req, stream)
}

func TestInvokeViewActionSharedActionName(t *testing.T) {
	views := `{"views": [{"plugin": "p", "id": "v", "title": "T", "blocks": [
	  {"type": "form", "id": "f", "inputs": [{"name": "note", "kind": "text"}], "submit": {"type": "button", "label": "Save", "action": "save", "value": "draft"}},
	  {"type": "actions", "elements": [{"type": "button", "label": "Publish", "action": "save", "value": "final"}]}]}]}`
	for _, c := range []struct {
		value, wantValue, wantForm string
		fields                     []string
	}{
		{value: "final", wantValue: "final"},
		{value: "draft", wantValue: "draft", fields: []string{"note=hi"}, wantForm: `{"note":"hi"}`},
	} {
		f := &fakeViewService{viewsJSON: views}
		s := startFake(t, f)
		req := actionRequest{sessionID: "s1", plugin: "p", viewID: "v", name: "save", value: ptr(c.value), fields: c.fields}
		if _, err := invokeViewAction(context.Background(), s, req); err != nil {
			t.Fatal(err)
		}
		if got := f.invokes[0]; got.GetValue() != c.wantValue || got.GetFormJson() != c.wantForm {
			t.Fatalf("--value %s sent %v", c.value, got)
		}
	}
}
