package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func withM1Server(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)
	original := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = original })
}
func m1TestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func TestM1ControlRPCPathsAuthAndResponse(t *testing.T) {
	for _, method := range []string{"PauseRun", "ResumeRun"} {
		t.Run(method, func(t *testing.T) {
			withM1Server(t, func(w http.ResponseWriter, r *http.Request) {
				assertAuthAndOrg(t, r.Header)
				if r.Method != "POST" || r.URL.Path != ciM1ServicePath+method || r.Header.Get("Connect-Protocol-Version") != "1" {
					t.Errorf("unexpected method/path/protocol: %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body) != 1 || body["runId"] != "run-123" {
					t.Errorf("unexpected body: %v", body)
				}
				fmt.Fprint(w, `{"runId":"run-123","paused":true,"wakePending":true,"revision":"42","futureField":"preserved"}`)
			})
			call := CIPauseRun
			if method == "ResumeRun" {
				call = CIResumeRun
			}
			resp, err := call(m1TestContext(t), "token-123", "org-123", "run-123")
			if err != nil {
				t.Fatal(err)
			}
			if resp.RunID != "run-123" || !resp.Paused || !resp.WakePending || string(resp.Revision) != `"42"` || !strings.Contains(string(resp.Raw), "futureField") {
				t.Fatalf("bad response: %+v", resp)
			}
		})
	}
}
func TestM1InputRPCFieldsOnlyAndUnknownMetadata(t *testing.T) {
	withM1Server(t, func(w http.ResponseWriter, r *http.Request) {
		assertAuthAndOrg(t, r.Header)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case ciM1ServicePath + "SubmitJobInput":
			if len(body) != 3 || body["jobId"] != "job-123" || body["attemptId"] != "attempt-123" {
				t.Errorf("unexpected payload: %v", body)
			}
			if body["fields"].(map[string]any)["decision"] != "approve" {
				t.Errorf("bad fields")
			}
			fmt.Fprint(w, `{"pending":true,"completed":false,"approvalCount":1,"future":"safe"}`)
		case ciM1ServicePath + "GetJob":
			if len(body) != 1 || body["jobId"] != "job-123" {
				t.Errorf("unexpected payload: %v", body)
			}
			fmt.Fprint(w, `{"jobId":"job-123","inputRequest":{"title":"Question","metadataJson":"{\"quorum\":2}"}}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	})
	ctx := m1TestContext(t)
	resp, err := CISubmitJobInput(ctx, "token-123", "org-123", "job-123", "attempt-123", map[string]string{"decision": "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Pending || resp.Completed || resp.ApprovalCount != 1 || !strings.Contains(string(resp.Raw), "future") {
		t.Fatalf("bad response: %+v", resp)
	}
	job, err := CIGetJobPublicJSON(ctx, "token-123", "org-123", "job-123")
	if err != nil || !strings.Contains(string(job), "metadataJson") {
		t.Fatalf("job = %s, error = %v", job, err)
	}
}
func TestM1SourceRESTPathsAndContracts(t *testing.T) {
	calls := 0
	definition := "name: planet\n"
	withM1Server(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		assertAuthAndOrg(t, r.Header)
		switch {
		case r.Method == "POST" && r.URL.Path == ciSourcesPath:
			var req CICreateEventSourceRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Repo != "chains" || req.Slug != "planet" || req.Definition != definition {
				t.Errorf("bad source request: %+v", req)
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "source-123", "url": "https://example.test/credential", "sourceFile": CIEventSourceFile{Path: ".depot/sources/planet.yml", Content: definition}})
		case r.Method == "POST" && r.URL.Path == ciSourcesPath+"/source-123/token":
			fmt.Fprint(w, `{"url":"https://example.test/new-credential"}`)
		case r.Method == "DELETE" && r.URL.Path == ciSourcesPath+"/source-123":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "GET" && r.URL.Path == ciSourcesPath:
			if r.URL.Query().Get("repo") != "chains/a & b" {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"sources":[{"id":"source-123"}]}`)
		case r.Method == "POST" && r.URL.Path == ciSourcesPath+"/source-123/recatch":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if len(req) != 2 || req["evidence"] != "Verified upstream" {
				t.Errorf("bad recatch: %v", req)
			}
			if req["body"].(map[string]any)["actor"] != "untrusted" {
				t.Error("body must remain inert")
			}
			fmt.Fprint(w, `{"accepted":true}`)
		default:
			t.Errorf("unexpected endpoint: %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := m1TestContext(t)
	source, err := CICreateEventSource(ctx, "token-123", "org-123", CICreateEventSourceRequest{Repo: "chains", Slug: "planet", Definition: definition})
	if err != nil || source.SourceFile.Content != definition {
		t.Fatalf("source = %+v, error = %v", source, err)
	}
	rotated, err := CIRotateEventSource(ctx, "token-123", "org-123", "source-123")
	if err != nil || rotated.URL == "" {
		t.Fatalf("rotated = %+v, error = %v", rotated, err)
	}
	revoked, err := CIRevokeEventSource(ctx, "token-123", "org-123", "source-123")
	if err != nil || string(revoked) != `{}` {
		t.Fatalf("revoke = %s, error = %v", revoked, err)
	}
	if _, err := CIListEventSources(ctx, "token-123", "org-123", "chains/a & b"); err != nil {
		t.Fatal(err)
	}
	if _, err := CIRecatchEventSource(ctx, "token-123", "org-123", "source-123", CIRecatchEventSourceRequest{Body: json.RawMessage(`{"actor":"untrusted"}`), Evidence: "Verified upstream"}); err != nil {
		t.Fatal(err)
	}
	if calls != 5 {
		t.Fatalf("calls = %d", calls)
	}
}
func TestM1JSONFailureBoundsAndNoSecrets(t *testing.T) {
	const secret = "credential-never-log"
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"http", 403, `{"message":"` + secret + `"}`, "HTTP 403"},
		{"truncated", 200, `{"credential":"` + secret + `"`, "invalid CI JSON"},
		{"malformed", 200, secret, "invalid CI JSON"},
		{"nonobject", 200, `null`, "invalid CI JSON"},
		{"trailing", 200, `{} {"credential":"` + secret + `"}`, "invalid CI JSON"},
		{"oversized", 200, `{"value":"` + strings.Repeat("x", ciM1ResponseMaxBytes) + secret + `"}`, "exceeds 1 MiB"},
		{"typed", 200, `{"paused":"` + secret + `"}`, "invalid CI control response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withM1Server(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tt.status); fmt.Fprint(w, tt.body) })
			_, err := CIPauseRun(m1TestContext(t), secret, "org-123", "run-123")
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), secret) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
func TestM1RejectsRedirectsAndHonorsCancellation(t *testing.T) {
	withM1Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://example.test/secret")
		w.WriteHeader(302)
	})
	_, err := CIPauseRun(m1TestContext(t), "fake-token", "org", "run-123")
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = CIPauseRun(ctx, "fake-token", "org", "run-123")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
func TestM1RejectsInvalidParametersAndOversizedRequests(t *testing.T) {
	withM1Server(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid input made a request") })
	ctx := m1TestContext(t)
	for _, id := range []string{"", "../token", "a/b", strings.Repeat("a", 201)} {
		if _, err := CIPauseRun(ctx, "fake-token", "org", id); err == nil {
			t.Error("expected invalid ID")
		}
		if _, err := CIRotateEventSource(ctx, "fake-token", "org", id); err == nil {
			t.Error("expected invalid ID")
		}
	}
	if _, err := CISubmitJobInput(ctx, "fake-token", "org", "job", "attempt", map[string]string{"value": strings.Repeat("x", ciM1RequestMaxBytes)}); err == nil {
		t.Error("expected request bound")
	}
	if _, err := CIRecatchEventSource(ctx, "fake-token", "org", "source", CIRecatchEventSourceRequest{Body: json.RawMessage(`{}`), Evidence: " "}); err == nil {
		t.Error("expected evidence error")
	}
	if _, err := CIRecatchEventSource(ctx, "fake-token", "org", "source", CIRecatchEventSourceRequest{Body: json.RawMessage(`{"x":"` + strings.Repeat("x", CIEventBodyMaxBytes) + `"}`), Evidence: "proof"}); err == nil {
		t.Error("expected body bound")
	}
	if _, err := CIRecatchEventSource(ctx, "fake-token", "org", "source", CIRecatchEventSourceRequest{Body: json.RawMessage(`{`), Evidence: "proof"}); err == nil {
		t.Error("expected malformed body error")
	}
}

func TestM1TruncatedHTTPBodyDoesNotEchoContent(t *testing.T) {
	withM1Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		fmt.Fprint(w, `{"secret":"credential-never-log"}`)
	})
	_, err := CIPauseRun(m1TestContext(t), "fake-token", "org", "run-123")
	if err == nil || strings.Contains(err.Error(), "credential-never-log") {
		t.Fatalf("expected sanitized truncated response error, got %v", err)
	}
}
func TestM1ContextDeadlineWhileReadingResponse(t *testing.T) {
	withM1Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := CIPauseRun(ctx, "fake-token", "org", "run-123")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline, got %v", err)
	}
}
