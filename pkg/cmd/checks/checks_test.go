package checks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"unicode/utf8"

	"connectrpc.com/connect"
	checksv1 "github.com/depot/cli/pkg/proto/depot/checks/v1"
	"github.com/depot/cli/pkg/proto/depot/checks/v1/checksv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
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
		var response attemptJSON
		if err := json.Unmarshal([]byte(out), &response); err != nil {
			t.Fatal(err)
		}
		if response.CheckID != "check" || response.AttemptID != "attempt" {
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
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "failed to get check:") {
		t.Fatalf("expected original denial, got %v", err)
	}
}

func TestAPIErrorsIncludeOperationAndPreserveStatus(t *testing.T) {
	mux := localAPI(t)
	denied := connect.NewError(connect.CodePermissionDenied, errors.New("access denied"))
	a := fixtureAttempt()
	a.Status = "failed"
	serveSelection(t, mux, a)
	serveRPC(t, mux, checksv1connect.CheckServiceListChecksProcedure, func(*checksv1.ListChecksRequest) (*checksv1.ListChecksResponse, error) { return nil, denied })
	serveRPC(t, mux, checksv1connect.CheckServiceListAttemptsProcedure, func(*checksv1.ListAttemptsRequest) (*checksv1.ListAttemptsResponse, error) { return nil, denied })
	serveRPC(t, mux, checksv1connect.CheckServiceRunCheckProcedure, func(*checksv1.RunCheckRequest) (*checksv1.RunCheckResponse, error) { return nil, denied })
	serveRPC(t, mux, checksv1connect.CheckServiceRetryCheckProcedure, func(*checksv1.RetryCheckRequest) (*checksv1.RetryCheckResponse, error) { return nil, denied })
	serveRPC(t, mux, checksv1connect.CheckServiceResolveFindingsProcedure, func(*checksv1.ResolveFindingsRequest) (*checksv1.GetCheckResponse, error) { return nil, denied })
	serveRPC(t, mux, checksv1connect.CheckServiceUnresolveFindingsProcedure, func(*checksv1.UnresolveFindingsRequest) (*checksv1.GetCheckResponse, error) { return nil, denied })
	serveRPC(t, mux, checksv1connect.CheckServiceGetCheckTranscriptProcedure, func(*checksv1.GetCheckTranscriptRequest) (*checksv1.GetCheckTranscriptResponse, error) {
		return nil, denied
	})
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceListReviewAgentsProcedure, func(*checksv1.ListReviewAgentsRequest) (*checksv1.ListReviewAgentsResponse, error) {
		return nil, denied
	})
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceGetReviewAgentProcedure, func(*checksv1.GetReviewAgentRequest) (*checksv1.GetReviewAgentResponse, error) { return nil, denied })
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceCreateReviewAgentProcedure, func(*checksv1.CreateReviewAgentRequest) (*checksv1.GetReviewAgentResponse, error) { return nil, denied })
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceUpdateReviewAgentProcedure, func(*checksv1.UpdateReviewAgentRequest) (*checksv1.GetReviewAgentResponse, error) { return nil, denied })
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceListReviewAgentModelsProcedure, func(*checksv1.ListReviewAgentModelsRequest) (*checksv1.ListReviewAgentModelsResponse, error) {
		return nil, denied
	})
	for _, scenario := range []struct {
		args      []string
		operation string
	}{
		{[]string{"list"}, "list checks"},
		{[]string{"attempts", "check"}, "list check attempts"},
		{[]string{"run", "agent", "--repo", "github:depot/api"}, "run check"},
		{[]string{"retry", "check"}, "retry check"},
		{[]string{"resolve", "check", "--finding", "0"}, "resolve findings"},
		{[]string{"unresolve", "check", "--all"}, "unresolve findings"},
		{[]string{"transcript", "check"}, "get check transcript"},
		{[]string{"agents", "list"}, "list review agents"},
		{[]string{"agents", "get", "agent"}, "get review agent"},
		{[]string{"agents", "create", "--name", "Security", "--instructions", "Review carefully"}, "create review agent"},
		{[]string{"agents", "update", "agent", "--model", "default"}, "update review agent"},
		{[]string{"agents", "models"}, "list review agent models"},
	} {
		out, err := execute(t, append(scenario.args, "-o", "json")...)
		if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.HasPrefix(err.Error(), "failed to "+scenario.operation+":") || out != "" {
			t.Errorf("%v: out %q, error %v", scenario.args, out, err)
		}
	}
}

func TestListTablesShortenDisplayCellsWithoutLosingDetails(t *testing.T) {
	mux := localAPI(t)
	a := fixtureAttempt()
	a.CheckId, a.Id = "check-"+strings.Repeat("c", 35), "attempt-"+strings.Repeat("a", 35)
	a.AgentName, a.Branch, a.HeadSha = strings.Repeat("界", 35), "feature/"+strings.Repeat("branch", 8), strings.Repeat("0123456789abcdef", 2)+"01234567"
	a.Repo = &checksv1.Repository{Forge: "github", Name: "depot/" + strings.Repeat("repository", 6)}
	serveSelection(t, mux, a)
	serveRPC(t, mux, checksv1connect.CheckServiceListChecksProcedure, func(*checksv1.ListChecksRequest) (*checksv1.ListChecksResponse, error) {
		return &checksv1.ListChecksResponse{Checks: []*checksv1.Check{{Id: a.CheckId, CurrentAttempt: a}}}, nil
	})
	agent := &checksv1.ReviewAgent{Id: "agent-" + strings.Repeat("g", 35), Name: a.AgentName, ModelId: strings.Repeat("model", 9), Repos: &checksv1.RepositoryList{Items: []*checksv1.Repository{a.Repo}}, Triggers: &checksv1.ReviewAgentTriggers{Events: []string{"github:synchronize", "cursor-origin:push", "code:push"}}}
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceListReviewAgentsProcedure, func(*checksv1.ListReviewAgentsRequest) (*checksv1.ListReviewAgentsResponse, error) {
		return &checksv1.ListReviewAgentsResponse{Agents: []*checksv1.ReviewAgent{agent}}, nil
	})
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceGetReviewAgentProcedure, func(*checksv1.GetReviewAgentRequest) (*checksv1.GetReviewAgentResponse, error) {
		return &checksv1.GetReviewAgentResponse{Agent: agent}, nil
	})
	for _, scenario := range []struct {
		list, get []string
		full      []string
		ids       []string
	}{
		{[]string{"list"}, []string{"get", a.Id}, []string{a.AgentName, a.Repo.Name, a.Branch, a.HeadSha}, []string{a.CheckId, a.Id}},
		{[]string{"agents", "list"}, []string{"agents", "get", agent.Id}, []string{agent.Name, a.Repo.Name, strings.Join(agent.Triggers.Events, ", ")}, []string{agent.Id, agent.ModelId}},
	} {
		out, err := execute(t, scenario.list...)
		if err != nil || !utf8.ValidString(out) || !strings.Contains(out, strings.Repeat("界", 29)+"…") {
			t.Fatalf("invalid shortened table: %s: %v", out, err)
		}
		if scenario.list[0] == "list" && strings.Fields(strings.Split(out, "\n")[1])[5] != a.HeadSha[:12] {
			t.Fatalf("expected 12-character SHA: %s", out)
		}
		for _, value := range scenario.full {
			if strings.Contains(out, value) {
				t.Errorf("list retained unbounded display cell %q", value)
			}
		}
		for _, id := range scenario.ids {
			if !strings.Contains(out, id) {
				t.Errorf("list shortened ID %q: %s", id, out)
			}
		}
		for _, args := range [][]string{scenario.get, append(scenario.list, "-o", "json")} {
			detailed, err := execute(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range scenario.full {
				if strings.Contains(value, ", ") && args[len(args)-1] == "json" {
					for _, event := range strings.Split(value, ", ") {
						if !strings.Contains(detailed, event) {
							t.Errorf("JSON lost event %q: %s", event, detailed)
						}
					}
					continue
				}
				if !strings.Contains(detailed, value) {
					t.Errorf("detailed/JSON output lost %q: %s", value, detailed)
				}
			}
		}
	}
}

func TestListCollectsUpToLimitAndPreservesFilters(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		args       []string
		pageCounts []int
		pageSizes  []uint32
		want       int
	}{
		{"default", nil, []int{1}, []uint32{50}, 1},
		{"limit", []string{"-n", "2"}, []int{1, 1, 1}, []uint32{2, 1}, 2},
		{"overfull-page", []string{"-n", "101"}, []int{100, 3}, []uint32{100, 1}, 101},
		{"empty-page", []string{"-n", "2"}, []int{0, 1}, []uint32{2, 2}, 1},
		{"no-matches", nil, []int{0}, []uint32{50}, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			mux := localAPI(t)
			var sizes []uint32
			serveRPC(t, mux, checksv1connect.CheckServiceListChecksProcedure, func(req *checksv1.ListChecksRequest) (*checksv1.ListChecksResponse, error) {
				page := len(sizes)
				if page >= len(scenario.pageCounts) {
					t.Error("fetched beyond the result limit")
					return nil, connect.NewError(connect.CodeInternal, errors.New("unexpected page"))
				}
				if req.AgentId != "agent" || req.GetRepo().GetForge() != "github" || req.GetRepo().GetName() != "depot/api" || req.Branch != "main" || req.Sha != "abc" || req.Outcome != "rejected" || req.Query != "security" {
					t.Errorf("lost filters: %v", req)
				}
				wantToken := ""
				if page > 0 {
					wantToken = fmt.Sprintf("page-%d", page)
				}
				if req.PageToken != wantToken {
					t.Errorf("page token %q, want %q", req.PageToken, wantToken)
				}
				sizes = append(sizes, req.PageSize)
				result := &checksv1.ListChecksResponse{}
				for i := 0; i < scenario.pageCounts[page]; i++ {
					result.Checks = append(result.Checks, &checksv1.Check{Id: fmt.Sprintf("check-%d-%d", page, i), CurrentAttempt: fixtureAttempt()})
				}
				if page+1 < len(scenario.pageCounts) {
					result.NextPageToken = fmt.Sprintf("page-%d", page+1)
				}
				return result, nil
			})
			args := []string{"list", "--agent", "agent", "--repo", "github:depot/api", "--branch", "main", "--sha", "abc", "--outcome", "rejected", "--query", "security", "-o", "json"}
			out, err := execute(t, append(args, scenario.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			var result []checkJSON
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			if len(result) != scenario.want || !reflect.DeepEqual(sizes, scenario.pageSizes) {
				t.Fatalf("checks %d, request sizes %v", len(result), sizes)
			}
		})
	}
}

func TestListsRejectPaginationFlagsBeforeAnyRequest(t *testing.T) {
	mux := localAPI(t)
	serveRPC(t, mux, checksv1connect.CheckServiceListChecksProcedure, func(*checksv1.ListChecksRequest) (*checksv1.ListChecksResponse, error) {
		t.Error("invalid pagination flags must not make an API request")
		return &checksv1.ListChecksResponse{}, nil
	})
	for _, command := range [][]string{{"list"}, {"ls"}, {"agents", "list"}, {"agents", "ls"}, {"attempts", "check"}} {
		for _, flag := range []string{"--all", "--page-token"} {
			out, err := execute(t, append(command, flag, "-o", "json")...)
			if err == nil || !strings.Contains(err.Error(), "unknown flag: "+flag) || out != "" {
				t.Fatalf("%s %s: out %q, error %v", command, flag, out, err)
			}
		}
	}
}

func TestAgentAndAttemptListsCollectTotalLimit(t *testing.T) {
	for _, kind := range []string{"agents", "attempts"} {
		for _, limit := range []int{3, 101} {
			t.Run(fmt.Sprintf("%s/%d", kind, limit), func(t *testing.T) {
				mux := localAPI(t)
				calls := 0
				page := func(token string, size uint32) int {
					calls++
					if calls == 1 {
						if token != "" || size != uint32(min(limit, 100)) {
							t.Errorf("first request: token %q, size %d", token, size)
						}
						return limit - 1
					}
					if calls != 2 || token != "next" || size != 1 {
						t.Errorf("continuation: call %d, token %q, size %d", calls, token, size)
					}
					return 2 // An overfull response must still respect the total limit.
				}
				args := []string{"agents", "list"}
				if kind == "agents" {
					serveRPC(t, mux, checksv1connect.ReviewAgentServiceListReviewAgentsProcedure, func(req *checksv1.ListReviewAgentsRequest) (*checksv1.ListReviewAgentsResponse, error) {
						resp := &checksv1.ListReviewAgentsResponse{NextPageToken: "next"}
						for i, count := 0, page(req.PageToken, req.PageSize); i < count; i++ {
							resp.Agents = append(resp.Agents, &checksv1.ReviewAgent{Id: fmt.Sprintf("agent-%d-%d", calls, i)})
						}
						return resp, nil
					})
				} else {
					args = []string{"attempts", "check"}
					serveSelection(t, mux, fixtureAttempt())
					serveRPC(t, mux, checksv1connect.CheckServiceListAttemptsProcedure, func(req *checksv1.ListAttemptsRequest) (*checksv1.ListAttemptsResponse, error) {
						if req.CheckId != "check" {
							t.Errorf("lost parent check: %v", req)
						}
						resp := &checksv1.ListAttemptsResponse{NextPageToken: "next"}
						for i, count := 0, page(req.PageToken, req.PageSize); i < count; i++ {
							a := fixtureAttempt()
							a.Id = fmt.Sprintf("attempt-%d-%d", calls, i)
							resp.Attempts = append(resp.Attempts, a)
						}
						return resp, nil
					})
				}
				out, err := execute(t, append(args, "-n", fmt.Sprint(limit), "-o", "json")...)
				if err != nil {
					t.Fatal(err)
				}
				var rows []map[string]json.RawMessage
				if err := json.Unmarshal([]byte(out), &rows); err != nil {
					t.Fatal(err)
				}
				key := "agent_id"
				if kind == "attempts" {
					key = "attempt_id"
				}
				if len(rows) != limit || calls != 2 || rows[0][key] == nil {
					t.Fatalf("rows %d, calls %d, first row %v", len(rows), calls, rows[0])
				}
			})
		}
	}
}

func TestCheckJSONPreservesResultsAndZeroFindingIndexes(t *testing.T) {
	for _, raw := range []string{fixtureAttempt().ResultJson, `{"version":99,"new_field":true}`, `invalid stored JSON`} {
		t.Run(raw, func(t *testing.T) {
			mux := localAPI(t)
			a := fixtureAttempt()
			a.IsCurrent, a.ResultJson = false, raw
			a.Resolutions = []*checksv1.FindingResolution{{FindingIndex: 0}}
			zero := uint32(0)
			a.PreviousReview = &checksv1.PreviousReviewOutcomes{AttemptId: "previous", Findings: []*checksv1.PreviousFindingOutcome{{FindingIndex: 0, Outcome: "still_open", CurrentFindingIndex: &zero}}}
			serveSelection(t, mux, a)
			out, err := execute(t, "get", "attempt", "-o", "json")
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				CheckID     string                       `json:"check_id"`
				AttemptID   string                       `json:"attempt_id"`
				Result      json.RawMessage              `json:"result"`
				ResultJSON  string                       `json:"result_json"`
				Resolutions []map[string]json.RawMessage `json:"resolutions"`
				Previous    struct {
					Findings []map[string]json.RawMessage `json:"findings"`
				} `json:"previous_review"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			if result.CheckID != "check" || result.AttemptID != "attempt" || result.ResultJSON != raw || (len(result.Result) > 0) != json.Valid([]byte(raw)) {
				t.Fatalf("lost identities or original/structured result: %s", out)
			}
			if string(result.Resolutions[0]["finding_index"]) != "0" || string(result.Previous.Findings[0]["finding_index"]) != "0" || string(result.Previous.Findings[0]["current_finding_index"]) != "0" {
				t.Fatalf("lost zero-based indexes: %s", out)
			}
		})
	}
}

func TestListPaginationErrorsDoNotPrintPartialResults(t *testing.T) {
	for _, scenario := range []string{"repeated-token", "api-error"} {
		t.Run(scenario, func(t *testing.T) {
			mux := localAPI(t)
			serveRPC(t, mux, checksv1connect.CheckServiceListChecksProcedure, func(req *checksv1.ListChecksRequest) (*checksv1.ListChecksResponse, error) {
				if scenario == "api-error" && req.PageToken != "" {
					return nil, connect.NewError(connect.CodeUnavailable, errors.New("unavailable"))
				}
				return &checksv1.ListChecksResponse{Checks: []*checksv1.Check{{Id: "check"}}, NextPageToken: "same"}, nil
			})
			out, err := execute(t, "list", "-n", "3", "-o", "json")
			if err == nil || out != "" {
				t.Fatalf("out %q, error %v", out, err)
			}
			if scenario == "api-error" && connect.CodeOf(err) != connect.CodeUnavailable {
				t.Fatalf("lost API error: %v", err)
			}
			if scenario == "repeated-token" && !strings.Contains(err.Error(), "repeated page token") {
				t.Fatalf("wrong error: %v", err)
			}
		})
	}
}

func TestAgentListRejectsRepeatedPageToken(t *testing.T) {
	mux := localAPI(t)
	serveRPC(t, mux, checksv1connect.ReviewAgentServiceListReviewAgentsProcedure, func(*checksv1.ListReviewAgentsRequest) (*checksv1.ListReviewAgentsResponse, error) {
		return &checksv1.ListReviewAgentsResponse{NextPageToken: "same"}, nil
	})
	out, err := execute(t, "agents", "list", "-n", "3", "-o", "json")
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
		if req.CheckId != "check" || req.PageToken != "" || req.PageSize != 2 {
			t.Errorf("wrong history selection: %v", req)
		}
		return &checksv1.ListAttemptsResponse{Attempts: []*checksv1.CheckAttempt{a}}, nil
	})
	out, err := execute(t, "attempts", "old-attempt", "-n", "2", "-o", "json")
	if err != nil || !strings.Contains(out, "old-attempt") {
		t.Fatalf("%s: %v", out, err)
	}
	out, err = execute(t, "get", "old-attempt", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var response attemptJSON
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if response.IsCurrent || response.AttemptID != "old-attempt" {
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
	args := []string{"run", "agent", "--repo", "cursor-origin:owner/repo", "--ref", "feature", "--commit-sha", strings.Repeat("a", 40), "--base", "main"}
	out, err := execute(t, append(args, "-o", "json")...)
	if err != nil {
		t.Fatal(err)
	}
	var ids map[string]string
	if err := json.Unmarshal([]byte(out), &ids); err != nil {
		t.Fatal(err)
	}
	if ids["check_id"] != "new-check" || ids["attempt_id"] != "new-attempt" {
		t.Fatalf("missing identities: %s", out)
	}
	out, err = execute(t, args...)
	if err != nil || !strings.Contains(out, "depot checks get new-attempt --org test-org") {
		t.Fatalf("lost organization in follow-up: %s: %v", out, err)
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
			if scenario.allowed {
				var ids map[string]string
				if err := json.Unmarshal([]byte(out), &ids); err != nil || ids["check_id"] != "check" || ids["attempt_id"] != "retry-attempt" {
					t.Fatalf("missing retry identities: %s: %v", out, err)
				}
				out, err = execute(t, "retry", "attempt")
				if err != nil || !strings.Contains(out, "depot checks get retry-attempt --org test-org") {
					t.Fatalf("lost organization in follow-up: %s: %v", out, err)
				}
			} else if !scenario.current && !strings.Contains(err.Error(), "depot checks get check --org test-org") {
				t.Fatalf("lost organization in stale-attempt guidance: %v", err)
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
	if _, err := execute(t, "resolve", "attempt", "--all"); err == nil || !strings.Contains(err.Error(), "depot checks get check --org test-org") {
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
	for _, args := range [][]string{{"agents", "list"}, {"agents", "list", "-n", "3", "-o", "json"}, {"agents", "get", "agent"}, {"agents", "models"}} {
		if out, err := execute(t, args...); err != nil || out == "" {
			t.Fatalf("%v: %s: %v", args, out, err)
		}
	}
}

func TestInvalidInputsFailBeforeAnyRequest(t *testing.T) {
	localAPI(t) // No registered routes: a request would produce a different error.
	for _, args := range [][]string{
		{"list", "--branch", "main"}, {"list", "-n", "0"}, {"agents", "list", "-n", "0"}, {"attempts", "check", "-n", "-1"}, {"list", "--outcome", "bogus"}, {"list", "-o", "yaml"},
		{"run", "agent", "--repo", "github:depot/api", "--head", "main", "--ref", "feature"}, {"run", "agent", "--repo", "invalid:repo"},
		{"agents", "update", "agent"}, {"agents", "create", "--name", "Security"}, {"agents", "update", "agent", "--instructions", ""},
		{"agents", "update", "agent", "--clear-repos", "--repo", "code:tools"}, {"agents", "update", "agent", "--clear-events", "--event", "code:push"},
		{"agents", "create", "--name", "A", "--instructions", "B", "--instructions-file", "missing"},
		{"resolve", "check"}, {"resolve", "check", "--all", "--finding", "0"}, {"unresolve", "check", "--finding", "-1"},
		{"run", "agent", "--repo", "github:depot/api", "--wait"}, {"transcript", "check", "-o", "yaml"},
	} {
		_, err := execute(t, args...)
		if err == nil || strings.Contains(err.Error(), "unimplemented") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

const sessionArchive = "{\"type\":\"session\",\"version\":3,\"id\":\"session-1\",\"parentSession\":\"parent/session.jsonl\"}\n{\"type\":\"message\",\"id\":\"message-1\",\"parentId\":null,\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Review complete.\"}]}}\n"

func TestTranscriptVerifiesArchiveAndKeepsSignedURLPrivate(t *testing.T) {
	for _, scenario := range []string{"text", "json", "jsonl", "file", "json-file", "text-file", "rename-error", "digest-mismatch", "size-mismatch", "http-error", "expired", "historical"} {
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
			if scenario == "json" || scenario == "json-file" {
				args = append(args, "-o", "json")
			}
			if scenario == "jsonl" || scenario == "file" || strings.HasSuffix(scenario, "mismatch") {
				args = append(args, "-o", "jsonl")
			}
			if scenario == "file" || scenario == "json-file" || scenario == "text-file" || strings.HasSuffix(scenario, "mismatch") {
				if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--output-file", file)
			}
			if scenario == "rename-error" {
				if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--output-file", file)
			}
			cmd := NewCmdChecks()
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs(append(args, "--org", "test-org", "--token", "local-test-token"))
			err := cmd.Execute()
			out, diagnostics := stdout.String(), stderr.String()
			if strings.Contains(fmt.Sprint(err)+out+diagnostics, "private-signed-token") {
				t.Fatal("signed URL leaked")
			}
			if scenario == "expired" || scenario == "http-error" || scenario == "rename-error" || strings.HasSuffix(scenario, "mismatch") {
				if err == nil || out != "" || strings.Contains(diagnostics, "Saved transcript") {
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
			if (scenario == "jsonl" || scenario == "json") && (out != sessionArchive || diagnostics != "") {
				t.Fatalf("original bytes changed: %q", out)
			}
			if scenario == "file" || scenario == "json-file" || scenario == "text-file" {
				saved, err := os.ReadFile(file)
				if err != nil || out != "" || (scenario != "text-file" && string(saved) != sessionArchive) || (scenario == "text-file" && !strings.Contains(string(saved), "Review complete.")) {
					t.Fatalf("bad saved archive: %q, %v", saved, err)
				}
				want := fmt.Sprintf("Saved transcript to %s (%d bytes).\n", file, len(saved))
				if diagnostics != want {
					t.Fatalf("download confirmation %q, want %q", diagnostics, want)
				}
			} else if scenario != "jsonl" && scenario != "json" {
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
