package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/depot/cli/pkg/api"
	"github.com/spf13/cobra"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"gopkg.in/yaml.v3"
)

func m1CommandServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
}
func executeM1(t *testing.T, cmd *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd.SetContext(ctx)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}
func m1CheckAuth(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("Authorization") != "Bearer fake-token" || r.Header.Get("x-depot-org") != "org-test" {
		t.Error("missing expected test authentication")
	}
}
func m1SourceArgs() []string {
	return []string{"create", "--repo", "chains", "--slug", "planet", "--event", "planetscale.deploy_request", "--key", "database=request.body.database", "--key", "number=request.body.resource.number", "--state", "request.body.event", "--field", "branch=request.body.resource.branch", "--delivery-id", "request.body.id", "--org", "org-test", "--token", "fake-token"}
}

const m1SourceSnapshot = `name: "planet"
event: "planetscale.deploy_request"
key:
  "database": "${{ request.body.database }}"
  "number": "${{ request.body.resource.number }}"
state: "${{ request.body.event }}"
delivery-id: "${{ request.body.id }}"
fields:
  "branch": "${{ request.body.resource.branch }}"
`

func TestM1SourceYAMLSnapshotAndLiteralQuoting(t *testing.T) {
	got, err := buildSourceDefinition("planet", "planetscale.deploy_request", []string{"number=request.body.resource.number", "database=request.body.database"}, "request.body.event", []string{"branch=request.body.resource.branch"}, "request.body.id")
	if err != nil {
		t.Fatal(err)
	}
	if got != m1SourceSnapshot {
		t.Fatalf("YAML differs:\n%s", got)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["key"].(map[string]any)["database"] != "${{ request.body.database }}" {
		t.Error("reference was not inert string")
	}
	event := "false:\n- \"yes\" # not YAML structure"
	got, err = buildSourceDefinition("planet", event, []string{"id=${{request.body.id}}"}, "request.body.state", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	parsed = nil
	if err := yaml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["event"] != event || len(parsed) != 5 {
		t.Fatalf("literal became YAML: %v", parsed)
	}
}
func TestM1SourceCreateRedirectionNeverSavesCredential(t *testing.T) {
	const secretURL = "https://example.test/sources/source-test/fake-credential"
	calls := 0
	m1CommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		m1CheckAuth(t, r)
		if r.Method != "POST" || r.URL.Path != "/v1/ci/sources" {
			t.Errorf("unexpected endpoint: %s %s", r.Method, r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if len(req) != 3 || req["repo"] != "chains" || req["slug"] != "planet" || req["definition"] != m1SourceSnapshot {
			t.Errorf("bad request: %v", req)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "source-test", "url": secretURL, "sourceFile": map[string]any{"path": ".depot/sources/planet.yml", "content": req["definition"]}})
	})
	args := m1SourceArgs()
	out, errOut, err := executeM1(t, NewCmdSource(), args...)
	if err != nil {
		t.Fatal(err)
	}
	if out != m1SourceSnapshot || strings.Contains(out, secretURL) || strings.Count(errOut, secretURL) != 1 {
		t.Fatalf("stdout/stderr separation failed: %q / %q", out, errOut)
	}
	// Model shell redirection to a credential-free declaration file.
	path := filepath.Join(t.TempDir(), "source.yml")
	if err := os.WriteFile(path, []byte(out), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte("fake-credential")) {
		t.Fatalf("credential in file or read failure: %v", err)
	}
	out, errOut, err = executeM1(t, NewCmdSource(), append(args, "--output", "json")...)
	if err != nil || !json.Valid([]byte(out)) || strings.Count(out, secretURL) != 1 || strings.Contains(errOut, secretURL) || !strings.Contains(errOut, "credential URL") {
		t.Fatalf("JSON output failed: %q / %q / %v", out, errOut, err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}
func TestM1SourceRefusesUnvalidatedServerDeclaration(t *testing.T) {
	m1CommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": "source-test", "url": "credential-never-print", "sourceFile": map[string]any{"path": ".depot/sources/planet.yml", "content": "url: credential-never-print"}})
	})
	out, errOut, err := executeM1(t, NewCmdSource(), m1SourceArgs()...)
	if err == nil || out != "" || errOut != "" || strings.Contains(err.Error(), "credential-never-print") {
		t.Fatalf("unsafe output: %q / %q / %v", out, errOut, err)
	}
}
func TestM1SourceRotateRevokeList(t *testing.T) {
	const secretURL = "https://example.test/fake-new-credential"
	m1CommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		m1CheckAuth(t, r)
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/ci/sources/source-test/token":
			fmt.Fprintf(w, `{"url":%q}`, secretURL)
		case "DELETE /v1/ci/sources/source-test":
			fmt.Fprint(w, `{"id":"source-test","disabled":true}`)
		case "GET /v1/ci/sources":
			if r.URL.Query().Get("repo") != "chains" {
				t.Error("wrong repo")
			}
			fmt.Fprint(w, `{"sources":[{"id":"source-test"}]}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	out, errOut, err := executeM1(t, NewCmdSource(), "rotate", "source-test", "--org", "org-test", "--token", "fake-token")
	if err != nil || out != "" || strings.Count(errOut, secretURL) != 1 {
		t.Fatalf("rotation: %q / %q / %v", out, errOut, err)
	}
	out, _, err = executeM1(t, NewCmdSource(), "revoke", "source-test", "--org", "org-test", "--token", "fake-token")
	if err != nil || !strings.Contains(out, "revoked") {
		t.Fatalf("revoke: %q / %v", out, err)
	}
	out, _, err = executeM1(t, NewCmdSource(), "list", "--repo", "chains", "--org", "org-test", "--token", "fake-token", "--output", "json")
	if err != nil || !strings.Contains(out, "source-test") {
		t.Fatalf("list: %q / %v", out, err)
	}
}
func TestM1SourceRecatchUsesEvidenceNotClientActor(t *testing.T) {
	m1CommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		m1CheckAuth(t, r)
		if r.Method != "POST" || r.URL.Path != "/v1/ci/sources/source-test/recatch" {
			t.Error("wrong endpoint")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(payload) != 2 || payload["evidence"] != "Verified upstream" || payload["body"].(map[string]any)["actor"] != "untrusted" {
			t.Errorf("bad payload: %v", payload)
		}
		if _, ok := payload["actor"]; ok {
			t.Error("client claimed authenticated actor")
		}
		fmt.Fprint(w, `{"accepted":true}`)
	})
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(`{"actor":"untrusted","state":"done"}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, _, err := executeM1(t, NewCmdSource(), "recatch", "source-test", "--body-file", path, "--evidence", "Verified upstream", "--org", "org-test", "--token", "fake-token")
	if err != nil || !strings.Contains(out, "accepted") {
		t.Fatalf("recatch: %q / %v", out, err)
	}
}
func TestM1SourceValidation(t *testing.T) {
	m1CommandServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid input made request") })
	tests := []struct {
		name, slug, event, state, delivery string
		keys, fields                       []string
	}{
		{name: "slug traversal", slug: "../planet", event: "event", state: "request.body.state", keys: []string{"id=request.body.id"}},
		{name: "empty event", slug: "planet", state: "request.body.state", keys: []string{"id=request.body.id"}},
		{name: "event expression", slug: "planet", event: "${{ secrets.X }}", state: "request.body.state", keys: []string{"id=request.body.id"}},
		{name: "duplicate keys", slug: "planet", event: "event", state: "request.body.state", keys: []string{"id=request.body.id", "id=request.body.other"}},
		{name: "missing key equals", slug: "planet", event: "event", state: "request.body.state", keys: []string{"id"}},
		{name: "injected key", slug: "planet", event: "event", state: "request.body.state", keys: []string{"id:\n injected=request.body.id"}},
		{name: "interpolation", slug: "planet", event: "event", state: "request.body.state", keys: []string{"id=prefix${{ request.body.id }}"}},
		{name: "function", slug: "planet", event: "event", state: "request.body.state", keys: []string{"id=request.body.id.toString()"}},
		{name: "bracket path", slug: "planet", event: "event", state: "request.body.state", keys: []string{"id=request.body['id']"}},
		{name: "secret", slug: "planet", event: "event", state: "request.body.state", keys: []string{"id=${{ secrets.X }}"}},
		{name: "duplicate fields", slug: "planet", event: "event", state: "request.body.state", keys: []string{"id=request.body.id"}, fields: []string{"x=request.body.a", "x=request.body.b"}},
		{name: "invalid state", slug: "planet", event: "event", state: "request.body.state || true", keys: []string{"id=request.body.id"}},
		{name: "invalid delivery", slug: "planet", event: "event", state: "request.body.state", delivery: "${{ request.body.id }}suffix", keys: []string{"id=request.body.id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := buildSourceDefinition(tt.slug, tt.event, tt.keys, tt.state, tt.fields, tt.delivery); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	keys := make([]string, 33)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d=request.body.id", i)
	}
	if _, err := buildSourceDefinition("planet", "event", keys, "request.body.state", nil, ""); err == nil {
		t.Error("expected key count bound")
	}
	fields := make([]string, 40)
	for i := range fields {
		fields[i] = fmt.Sprintf("k%d=request.body.%s", i, strings.Repeat("x", 1800))
	}
	if _, err := buildSourceDefinition("planet", "event", []string{"id=request.body.id"}, "request.body.state", fields, ""); err == nil {
		t.Error("expected manifest size bound")
	}
	for _, body := range []string{`{`, `{} trailing`, `{"secret":"` + strings.Repeat("x", api.CIEventBodyMaxBytes) + `"}`} {
		path := filepath.Join(t.TempDir(), "event.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSourceBody(path); err == nil {
			t.Error("expected body error")
		}
	}
	if _, err := readSourceBody(t.TempDir()); err == nil {
		t.Error("expected regular file requirement")
	}
}
func TestM1ControlCommands(t *testing.T) {
	m1CommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		m1CheckAuth(t, r)
		if r.URL.Path != "/depot.ci.v1.CIService/PauseRun" && r.URL.Path != "/depot.ci.v1.CIService/ResumeRun" {
			t.Error("unexpected endpoint")
		}
		paused := strings.HasSuffix(r.URL.Path, "PauseRun")
		fmt.Fprintf(w, `{"runId":"run-test","paused":%t,"wakePending":true,"revision":"4","safeFuture":true}`, paused)
	})
	out, _, err := executeM1(t, NewCmdPause(), "run-test", "--org", "org-test", "--token", "fake-token")
	if err != nil || !strings.Contains(out, "paused=true, wake pending=true") {
		t.Fatalf("pause: %q / %v", out, err)
	}
	out, _, err = executeM1(t, NewCmdResume(), "run-test", "--org", "org-test", "--token", "fake-token", "--output", "json")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"paused": false`) || !strings.Contains(out, "safeFuture") {
		t.Fatalf("resume: %q / %v", out, err)
	}
}
func TestM1InputPendingCompletedAndLegacy(t *testing.T) {
	for _, tc := range []struct {
		name, response, status, want string
		gets                         int
		fields                       bool
	}{
		{"pending", `{"pending":true,"completed":false,"approvalCount":1}`, "pending", "Approval recorded; waiting for", 0, true},
		{"approved", `{"pending":false,"completed":true,"approvalCount":2}`, "finished", `"status": "finished"`, 1, true},
		{"rejected", `{"pending":false,"completed":true,"approvalCount":0}`, "failed", `"status": "failed"`, 1, true},
		{"legacy", `{}`, "finished", `"status": "finished"`, 1, true},
		{"inspect", `{}`, "pending", "metadataJson", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			submits, gets := 0, 0
			m1CommandServer(t, func(w http.ResponseWriter, r *http.Request) {
				m1CheckAuth(t, r)
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				switch r.URL.Path {
				case "/depot.ci.v1.CIService/SubmitJobInput":
					submits++
					if len(payload) != 3 || payload["jobId"] != "job-test" || payload["attemptId"] != "attempt-test" {
						t.Errorf("bad submit payload: %v", payload)
					}
					fmt.Fprint(w, tc.response)
				case "/depot.ci.v1.CIService/GetJob":
					gets++
					fmt.Fprintf(w, `{"jobId":"job-test","status":%q,"inputRequest":{"title":"Deploy?","metadataJson":"{}"}}`, tc.status)
				default:
					t.Error("unexpected RPC")
				}
			})
			args := []string{"job-test", "--org", "org-test", "--token", "fake-token"}
			if tc.fields {
				decision := "approve"
				if tc.name == "rejected" {
					decision = "reject"
				}
				args = append(args, "--attempt", "attempt-test", "--field", "decision="+decision)
			}
			out, _, err := executeM1(t, NewCmdInput(), args...)
			if err != nil || !strings.Contains(out, tc.want) || strings.Contains(strings.ToLower(out), "job complete") {
				t.Fatalf("output = %q, error = %v", out, err)
			}
			wantSubmits := 0
			if tc.fields {
				wantSubmits = 1
			}
			if submits != wantSubmits || gets != tc.gets {
				t.Fatalf("submits=%d gets=%d", submits, gets)
			}
		})
	}
}
func TestM1InputJSONAndFlagValidation(t *testing.T) {
	m1CommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "SubmitJobInput") {
			t.Error("unexpected metadata reread for JSON")
		}
		fmt.Fprint(w, `{"pending":true,"completed":false,"approvalCount":1,"safeFuture":true}`)
	})
	out, _, err := executeM1(t, NewCmdInput(), "job-test", "--attempt", "attempt-test", "--field", "decision=approve", "--org", "org-test", "--token", "fake-token", "--output", "json")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, "safeFuture") {
		t.Fatalf("JSON: %q / %v", out, err)
	}
	for _, args := range [][]string{{}, {"job-test", "--field", "decision=approve"}, {"job-test", "--field", "decision"}, {"job-test", "--attempt", "attempt", "--field", "decision=a", "--field", "decision=b"}, {"job-test", "--actor", "rob"}, {"job-test", "--sid", "human"}} {
		if _, _, err := executeM1(t, NewCmdInput(), args...); err == nil {
			t.Fatalf("expected flag error for %v", args)
		}
	}
	got, err := parseJobInputFields([]string{"action=a=b", "state=", "actor=untrusted-field"})
	if err != nil || !reflect.DeepEqual(got, map[string]string{"action": "a=b", "state": "", "actor": "untrusted-field"}) {
		t.Fatalf("all field keys must remain legal: %v / %v", got, err)
	}
	if _, err := parseJobInputFields([]string{"private-secret-without-equals"}); err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("invalid field echoed sensitive answer")
	}
}
func TestM1CommandsHTTPErrorNoSecretsAndRegistration(t *testing.T) {
	m1CommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":"fake-token secret-url"}`)
	})
	out, errOut, err := executeM1(t, NewCmdPause(), "run-test", "--org", "org-test", "--token", "fake-token")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || strings.Contains(err.Error(), "fake-token") || out != "" || errOut != "" {
		t.Fatalf("unsafe error: %q / %q / %v", out, errOut, err)
	}
	cmd := NewCmdCI()
	for _, verb := range []string{"source", "input", "pause", "resume", "status", "cancel"} {
		found, _, err := cmd.Find([]string{verb})
		if err != nil || found.Name() != verb {
			t.Fatalf("missing %s", verb)
		}
	}
}
