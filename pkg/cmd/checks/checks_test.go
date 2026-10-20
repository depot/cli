package checks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	checksv1 "github.com/depot/cli/pkg/proto/depot/checks/v1"
	"github.com/depot/cli/pkg/proto/depot/checks/v1/checksv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/encoding/protojson"
)

func localAPI(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	return mux
}

func serveRPC[Req, Resp any](t *testing.T, mux *http.ServeMux, path string, handle func(*Req) (*Resp, error)) {
	t.Helper()
	mux.Handle(path, connect.NewUnaryHandler(path, func(_ context.Context, req *connect.Request[Req]) (*connect.Response[Resp], error) {
		if req.Header().Get("Authorization") != "Bearer local-test-token" || req.Header().Get("x-depot-org") != "test-org" {
			t.Error("missing authentication or organization header")
		}
		if !strings.HasPrefix(req.Header().Get("User-Agent"), "depot-cli/") {
			t.Error("missing CLI user agent")
		}
		resp, err := handle(req.Msg)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(resp), nil
	}))
}

func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewCmdChecks()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append(args, "--org", "test-org", "--token", "local-test-token"))
	err := cmd.Execute()
	return out.String(), err
}

func fixtureAttempt() *checksv1.CheckAttempt {
	return &checksv1.CheckAttempt{
		Id: "attempt", CheckId: "check", IsCurrent: true, AgentId: "agent", AgentName: "Security",
		Repo:   &checksv1.Repository{Forge: "github", Name: "depot/api"},
		Status: "succeeded", HeadSha: "abc123", BaseSha: "def456", CreatedAt: "2026-10-20T20:00:00Z",
		ResultJson: `{"version":3,"decision":"fail","summary":"Check permissions.","findings":[{"title":"Missing authorization","severity":"high","path":"server.go","line":12,"body":"Check ownership first."}],"previous_attempt_id":null,"previous_findings":[]}`,
	}
}

func serveSelection(t *testing.T, mux *http.ServeMux, attempt *checksv1.CheckAttempt) {
	t.Helper()
	serveRPC(t, mux, checksv1connect.CheckServiceGetCheckProcedure, func(req *checksv1.GetCheckRequest) (*checksv1.GetCheckResponse, error) {
		if req.CheckId != "check" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("not found"))
		}
		return &checksv1.GetCheckResponse{Check: &checksv1.Check{Id: "check", CurrentAttempt: attempt}}, nil
	})
	serveRPC(t, mux, checksv1connect.CheckServiceGetAttemptProcedure, func(req *checksv1.GetAttemptRequest) (*checksv1.GetAttemptResponse, error) {
		if req.AttemptId != attempt.Id {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("not found"))
		}
		return &checksv1.GetAttemptResponse{Attempt: attempt}, nil
	})
}

func TestGetAcceptsCheckAndAttemptAndShowsBlockingFindings(t *testing.T) {
	mux := localAPI(t)
	serveSelection(t, mux, fixtureAttempt())
	for _, id := range []string{"check", "attempt"} {
		out, err := execute(t, "get", id)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"check", "attempt", "rejected", "Check permissions.", "[0] Missing authorization", "server.go:12", "Check ownership first."} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q: %s", want, out)
			}
		}
		out, err = execute(t, "get", id, "--output", "json")
		if err != nil {
			t.Fatal(err)
		}
		var response checksv1.GetAttemptResponse
		if err := protojson.Unmarshal([]byte(out), &response); err != nil {
			t.Fatal(err)
		}
		if response.GetAttempt().GetCheckId() != "check" || response.GetAttempt().GetId() != "attempt" {
			t.Fatalf("wrong identities: %s", out)
		}
	}
}

func TestGetPreservesAuthorizationErrors(t *testing.T) {
	mux := localAPI(t)
	serveRPC(t, mux, checksv1connect.CheckServiceGetCheckProcedure, func(*checksv1.GetCheckRequest) (*checksv1.GetCheckResponse, error) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("access denied"))
	})
	_, err := execute(t, "get", "check")
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("expected original denial, got %v", err)
	}
}

func TestListFiltersApplyToEveryPageAndReturnContinuation(t *testing.T) {
	mux := localAPI(t)
	var tokens []string
	serveRPC(t, mux, checksv1connect.CheckServiceListChecksProcedure, func(req *checksv1.ListChecksRequest) (*checksv1.ListChecksResponse, error) {
		if req.AgentId != "agent" || req.GetRepo().GetForge() != "github" || req.GetRepo().GetName() != "depot/api" || req.Branch != "main" || req.Sha != "abc" || req.Outcome != "rejected" || req.Query != "security" || req.PageSize != 1 {
			t.Errorf("lost filters: %v", req)
		}
		tokens = append(tokens, req.PageToken)
		result := &checksv1.ListChecksResponse{Checks: []*checksv1.Check{{Id: "check", CurrentAttempt: fixtureAttempt()}}}
		if req.PageToken == "" {
			result.NextPageToken = "next"
		}
		return result, nil
	})
	args := []string{"list", "--agent", "agent", "--repo", "github:depot/api", "--branch", "main", "--sha", "abc", "--outcome", "rejected", "--query", "security", "-n", "1", "-o", "json"}
	out, err := execute(t, args...)
	if err != nil {
		t.Fatal(err)
	}
	var page checksv1.ListChecksResponse
	if err := protojson.Unmarshal([]byte(out), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Checks) != 1 || page.NextPageToken != "next" {
		t.Fatalf("missing continuation: %s", out)
	}
	tokens = nil
	out, err = execute(t, append(args, "--all")...)
	if err != nil {
		t.Fatal(err)
	}
	if err := protojson.Unmarshal([]byte(out), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Checks) != 2 || page.NextPageToken != "" || !reflect.DeepEqual(tokens, []string{"", "next"}) {
		t.Fatalf("wrong pagination: %s; tokens %v", out, tokens)
	}
}

func TestAllRejectsRepeatedPageToken(t *testing.T) {
	mux := localAPI(t)
	serveRPC(t, mux, checksv1connect.CheckServiceListChecksProcedure, func(*checksv1.ListChecksRequest) (*checksv1.ListChecksResponse, error) {
		return &checksv1.ListChecksResponse{NextPageToken: "same"}, nil
	})
	out, err := execute(t, "list", "--all", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "repeated") || out != "" {
		t.Fatalf("out %q, error %v", out, err)
	}
}

func TestAttemptsUsesParentCheckAndHistoricalGetStaysExact(t *testing.T) {
	mux := localAPI(t)
	a := fixtureAttempt()
	a.Id, a.IsCurrent = "old-attempt", false
	serveSelection(t, mux, a)
	serveRPC(t, mux, checksv1connect.CheckServiceListAttemptsProcedure, func(req *checksv1.ListAttemptsRequest) (*checksv1.ListAttemptsResponse, error) {
		if req.CheckId != "check" || req.PageToken != "cursor" || req.PageSize != 2 {
			t.Errorf("wrong history selection: %v", req)
		}
		return &checksv1.ListAttemptsResponse{Attempts: []*checksv1.CheckAttempt{a}}, nil
	})
	out, err := execute(t, "attempts", "old-attempt", "--page-token", "cursor", "-n", "2", "-o", "json")
	if err != nil || !strings.Contains(out, "old-attempt") {
		t.Fatalf("%s: %v", out, err)
	}
	out, err = execute(t, "get", "old-attempt", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var response checksv1.GetAttemptResponse
	if err := protojson.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if response.Attempt.IsCurrent || response.Attempt.Id != "old-attempt" {
		t.Fatal("historical read retargeted")
	}
}

func TestRunQueuesOneOffComparisonAndReturnsBothIDs(t *testing.T) {
	mux := localAPI(t)
	serveRPC(t, mux, checksv1connect.CheckServiceRunCheckProcedure, func(req *checksv1.RunCheckRequest) (*checksv1.RunCheckResponse, error) {
		if req.AgentId != "agent" || req.GetRepo().GetForge() != "cursor-origin" || req.GetRepo().GetName() != "owner/repo" || req.GetRef() != "feature" || req.GetCommitSha() != strings.Repeat("a", 40) || req.GetBase() != "main" || req.Head != nil {
			t.Errorf("wrong admission: %v", req)
		}
		return &checksv1.RunCheckResponse{CheckId: "new-check", AttemptId: "new-attempt"}, nil
	})
	out, err := execute(t, "run", "agent", "--repo", "cursor-origin:owner/repo", "--ref", "feature", "--commit-sha", strings.Repeat("a", 40), "--base", "main", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "new-check") || !strings.Contains(out, "new-attempt") {
		t.Fatalf("missing identities: %s", out)
	}
}

func TestRetryOnlyAdmitsCurrentExecutionFailure(t *testing.T) {
	for _, scenario := range []struct {
		status  string
		current bool
		allowed bool
	}{{"failed", true, true}, {"failed", false, false}, {"succeeded", true, false}, {"running", true, false}, {"queued", true, false}} {
		t.Run(fmt.Sprintf("%s/current=%t", scenario.status, scenario.current), func(t *testing.T) {
			mux := localAPI(t)
			a := fixtureAttempt()
			a.Status, a.IsCurrent = scenario.status, scenario.current
			serveSelection(t, mux, a)
			called := false
			serveRPC(t, mux, checksv1connect.CheckServiceRetryCheckProcedure, func(req *checksv1.RetryCheckRequest) (*checksv1.RetryCheckResponse, error) {
				called = true
				if req.CheckId != "check" {
					t.Errorf("wrong retry: %v", req)
				}
				return &checksv1.RetryCheckResponse{CheckId: "check", AttemptId: "retry-attempt"}, nil
			})
			out, err := execute(t, "retry", "attempt", "-o", "json")
			if called != scenario.allowed || (err == nil) != scenario.allowed {
				t.Fatalf("called %t, error %v", called, err)
			}
			if scenario.allowed && !strings.Contains(out, "retry-attempt") {
				t.Fatalf("missing retry identity: %s", out)
			}
		})
	}
}

func TestFindingChangesPinSelectedAttemptAndKeepOriginalIndexes(t *testing.T) {
	mux := localAPI(t)
	a := fixtureAttempt()
	serveSelection(t, mux, a)
	serveRPC(t, mux, checksv1connect.CheckServiceResolveFindingsProcedure, func(req *checksv1.ResolveFindingsRequest) (*checksv1.GetCheckResponse, error) {
		if req.CheckId != "check" || req.AttemptId != "attempt" || req.All || !reflect.DeepEqual(req.FindingIndexes, []uint32{0, 2}) {
			t.Errorf("wrong resolution: %v", req)
		}
		return &checksv1.GetCheckResponse{Check: &checksv1.Check{Id: "check", CurrentAttempt: a}}, nil
	})
	serveRPC(t, mux, checksv1connect.CheckServiceUnresolveFindingsProcedure, func(req *checksv1.UnresolveFindingsRequest) (*checksv1.GetCheckResponse, error) {
		if req.CheckId != "check" || req.AttemptId != "attempt" || !req.All || len(req.FindingIndexes) != 0 {
			t.Errorf("wrong unresolution: %v", req)
		}
		return &checksv1.GetCheckResponse{Check: &checksv1.Check{Id: "check", CurrentAttempt: a}}, nil
	})
	if _, err := execute(t, "resolve", "check", "--finding", "0", "--finding", "2"); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "unresolve", "attempt", "--all", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	a.IsCurrent = false
	if _, err := execute(t, "resolve", "attempt", "--all"); err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("historical mutation allowed: %v", err)
	}
}

func TestAgentUpdatePreservesOmissionsAndSupportsClearing(t *testing.T) {
	mux := localAPI(t)
	var saved *checksv1.UpdateReviewAgentRequest
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceUpdateReviewAgentProcedure, func(req *checksv1.UpdateReviewAgentRequest) (*checksv1.GetReviewAgentResponse, error) {
		saved = req
		return &checksv1.GetReviewAgentResponse{Agent: &checksv1.ReviewAgent{Id: req.AgentId, Name: "Security"}}, nil
	})
	if _, err := execute(t, "agents", "update", "agent", "--instructions", "New instructions", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	if saved.AgentId != "agent" || saved.GetInstructions() != "New instructions" || saved.Name != nil || saved.ModelId != nil || saved.Repos != nil || saved.Triggers != nil {
		t.Fatalf("omitted settings overwritten: %v", saved)
	}
	if _, err := execute(t, "agents", "update", "agent", "--clear-repos", "--clear-events", "--model", "default"); err != nil {
		t.Fatal(err)
	}
	if saved.Repos == nil || saved.Triggers == nil || saved.ModelId == nil || saved.GetModelId() != "" || len(saved.Repos.Items) != 0 || len(saved.Triggers.Events) != 0 || saved.Instructions != nil {
		t.Fatalf("clear/reset lost presence: %v", saved)
	}
	if _, err := execute(t, "agents", "update", "agent", "--repo", "github:depot/api", "--repo", "code:tools", "--event", "code:push"); err != nil {
		t.Fatal(err)
	}
	if len(saved.Repos.Items) != 2 || saved.Repos.Items[1].Forge != "code" || !reflect.DeepEqual(saved.Triggers.Events, []string{"code:push"}) {
		t.Fatalf("mixed-forge update lost: %v", saved)
	}
}

func TestAgentCreateAcceptsInstructionFileAndStdin(t *testing.T) {
	mux := localAPI(t)
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceCreateReviewAgentProcedure, func(req *checksv1.CreateReviewAgentRequest) (*checksv1.GetReviewAgentResponse, error) {
		if req.Name != "Security" || req.Instructions != "Review carefully.\n" || req.ModelId != "" || req.Repos != nil || req.Triggers != nil {
			t.Errorf("wrong defaults: %v", req)
		}
		return &checksv1.GetReviewAgentResponse{Agent: &checksv1.ReviewAgent{Id: "agent", Name: req.Name, Instructions: req.Instructions}}, nil
	})
	file := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(file, []byte("Review carefully.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := execute(t, "agents", "create", "--name", "Security", "--instructions-file", file); err != nil || !strings.Contains(out, "Review carefully.") {
		t.Fatalf("%s: %v", out, err)
	}
	cmd := NewCmdChecks()
	cmd.SetIn(strings.NewReader("Review carefully.\n"))
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"agents", "create", "--name", "Security", "--instructions-file", "-", "--org", "test-org", "--token", "local-test-token"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentReadsAndModels(t *testing.T) {
	mux := localAPI(t)
	agent := &checksv1.ReviewAgent{Id: "agent", Name: "Security", Instructions: "Review carefully."}
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceListReviewAgentsProcedure, func(req *checksv1.ListReviewAgentsRequest) (*checksv1.ListReviewAgentsResponse, error) {
		resp := &checksv1.ListReviewAgentsResponse{Agents: []*checksv1.ReviewAgent{agent}}
		if req.PageToken == "" {
			resp.NextPageToken = "agents-next"
		}
		return resp, nil
	})
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceGetReviewAgentProcedure, func(req *checksv1.GetReviewAgentRequest) (*checksv1.GetReviewAgentResponse, error) {
		if req.AgentId != "agent" {
			t.Errorf("wrong agent: %v", req)
		}
		return &checksv1.GetReviewAgentResponse{Agent: agent}, nil
	})
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceListReviewAgentModelsProcedure, func(*checksv1.ListReviewAgentModelsRequest) (*checksv1.ListReviewAgentModelsResponse, error) {
		return &checksv1.ListReviewAgentModelsResponse{DefaultModelId: "model", Models: []*checksv1.ReviewAgentModel{{Id: "model", Label: "Review Model"}}}, nil
	})
	for _, args := range [][]string{{"agents", "list"}, {"agents", "list", "--all", "-o", "json"}, {"agents", "get", "agent"}, {"agents", "models"}} {
		if out, err := execute(t, args...); err != nil || out == "" {
			t.Fatalf("%v: %s: %v", args, out, err)
		}
	}
}

func TestInvalidInputsFailBeforeAnyRequest(t *testing.T) {
	localAPI(t) // No registered routes: a request would produce a different error.
	for _, args := range [][]string{
		{"list", "--branch", "main"}, {"list", "-n", "0"}, {"list", "--outcome", "bogus"}, {"list", "-o", "yaml"},
		{"run", "agent", "--repo", "github:depot/api", "--head", "main", "--ref", "feature"}, {"run", "agent", "--repo", "invalid:repo"},
		{"agents", "update", "agent"}, {"agents", "create", "--name", "Security"}, {"agents", "update", "agent", "--instructions", ""},
		{"agents", "update", "agent", "--clear-repos", "--repo", "code:tools"}, {"agents", "update", "agent", "--clear-events", "--event", "code:push"},
		{"agents", "create", "--name", "A", "--instructions", "B", "--instructions-file", "missing"},
		{"resolve", "check"}, {"resolve", "check", "--all", "--finding", "0"}, {"unresolve", "check", "--finding", "-1"},
		{"run", "agent", "--repo", "github:depot/api", "--wait"}, {"transcript", "check", "-o", "json"},
	} {
		_, err := execute(t, args...)
		if err == nil || strings.Contains(err.Error(), "unimplemented") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

const sessionArchive = "{\"type\":\"session\",\"version\":3,\"id\":\"session-1\",\"parentSession\":\"parent/session.jsonl\"}\n{\"type\":\"message\",\"id\":\"message-1\",\"parentId\":null,\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Review complete.\"}]}}\n"

func TestTranscriptVerifiesArchiveAndKeepsSignedURLPrivate(t *testing.T) {
	for _, scenario := range []string{"text", "jsonl", "file", "digest-mismatch", "size-mismatch", "http-error", "expired", "historical"} {
		t.Run(scenario, func(t *testing.T) {
			archive := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("x-depot-org") != "" {
					t.Error("API credentials sent to archive host")
				}
				if scenario == "http-error" {
					w.WriteHeader(403)
					return
				}
				io.WriteString(w, sessionArchive)
			}))
			t.Cleanup(archive.Close)
			transport := http.DefaultTransport
			http.DefaultTransport = archive.Client().Transport
			t.Cleanup(func() { http.DefaultTransport = transport })
			mux := localAPI(t)
			a := fixtureAttempt()
			if scenario == "historical" {
				a.Id, a.IsCurrent = "old-attempt", false
			}
			serveSelection(t, mux, a)
			sha := sha256.Sum256([]byte(sessionArchive))
			meta := &checksv1.GetCheckTranscriptResponse{Status: "archived", DownloadUrl: archive.URL + "/archive?secret=private-signed-token", Bytes: uint64(len(sessionArchive)), Sha256: hex.EncodeToString(sha[:])}
			if scenario == "digest-mismatch" {
				meta.Sha256 = strings.Repeat("0", 64)
			}
			if scenario == "size-mismatch" {
				meta.Bytes++
			}
			if scenario == "expired" {
				meta.Status, meta.Reason = "expired", "retention window elapsed"
			}
			serveRPC(t, mux, checksv1connect.CheckServiceGetCheckTranscriptProcedure, func(req *checksv1.GetCheckTranscriptRequest) (*checksv1.GetCheckTranscriptResponse, error) {
				if req.CheckId != "check" || req.AttemptId != a.Id {
					t.Errorf("transcript not pinned: %v", req)
				}
				return meta, nil
			})
			file := filepath.Join(t.TempDir(), "transcript.jsonl")
			args := []string{"transcript", a.Id}
			if scenario == "jsonl" || scenario == "file" || strings.HasSuffix(scenario, "mismatch") {
				args = append(args, "-o", "jsonl")
			}
			if scenario == "file" || strings.HasSuffix(scenario, "mismatch") {
				if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--output-file", file)
			}
			out, err := execute(t, args...)
			if strings.Contains(fmt.Sprint(err)+out, "private-signed-token") {
				t.Fatal("signed URL leaked")
			}
			if scenario == "expired" || scenario == "http-error" || strings.HasSuffix(scenario, "mismatch") {
				if err == nil || out != "" {
					t.Fatalf("invalid archive was emitted: %q, %v", out, err)
				}
				if strings.HasSuffix(scenario, "mismatch") {
					saved, _ := os.ReadFile(file)
					if string(saved) != "original" {
						t.Fatal("failed download replaced file")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "jsonl" && out != sessionArchive {
				t.Fatalf("original bytes changed: %q", out)
			}
			if scenario == "file" {
				saved, err := os.ReadFile(file)
				if err != nil || string(saved) != sessionArchive || out != "" {
					t.Fatalf("bad saved archive: %q, %v", saved, err)
				}
			} else if scenario != "jsonl" {
				for _, want := range []string{"Review complete.", "session-1", "parent/session.jsonl", "message-1; parent root"} {
					if !strings.Contains(out, want) {
						t.Errorf("human transcript lost %q: %s", want, out)
					}
				}
			}
		})
	}
}

func TestInspectionDistinguishesFailureDecisionAndResolution(t *testing.T) {
	for _, scenario := range []struct {
		name, status, result, want string
		resolved                   bool
	}{
		{"execution failure", "failed", "", "failed", false},
		{"legacy pass", "succeeded", `{"version":1,"summary":"OK","findings":[]}`, "passed", false},
		{"pass with advice", "succeeded", `{"version":2,"decision":"pass","summary":"OK","findings":[{"title":"Advice"}]}`, "findings", false},
		{"rejected without findings", "succeeded", `{"version":2,"decision":"fail","summary":"No","findings":[]}`, "rejected", false},
		{"resolved findings", "succeeded", fixtureAttempt().ResultJson, "resolved", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			mux := localAPI(t)
			a := fixtureAttempt()
			a.Status, a.ResultJson = scenario.status, scenario.result
			if scenario.resolved {
				a.Resolutions = []*checksv1.FindingResolution{{FindingIndex: 0, ResolvedAt: "2026-10-20T21:00:00Z"}}
			}
			serveSelection(t, mux, a)
			out, err := execute(t, "get", "check")
			if err != nil || !strings.Contains(out, scenario.want) {
				t.Fatalf("%s: %v", out, err)
			}
		})
	}
}
