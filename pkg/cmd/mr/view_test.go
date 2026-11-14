package mr

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
)

func viewRPC(t *testing.T, head string, readiness *code.MergeRequestReadiness) {
	t.Helper()
	rpc(t, apiServer(t), codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if !req.IncludeReadiness {
			t.Error("view did not ask for readiness")
		}
		return &code.GetMergeRequestResponse{MergeRequest: fixture(head), Readiness: readiness}, nil
	})
}

func TestViewPrintsReadiness(t *testing.T) {
	passed, failed := code.MergeRequestCheckState_MERGE_REQUEST_CHECK_STATE_PASSED, code.MergeRequestCheckState_MERGE_REQUEST_CHECK_STATE_FAILED
	reason := func(kind code.MergeRequestReadiness_Reason_Kind, count uint32) *code.MergeRequestReadiness_Reason {
		return &code.MergeRequestReadiness_Reason{Kind: kind, Count: count}
	}
	for _, test := range []struct {
		name      string
		readiness *code.MergeRequestReadiness
		want      string
	}{
		{name: "ready", readiness: &code.MergeRequestReadiness{
			Verdict: code.MergeRequestReadiness_VERDICT_READY, Mergeability: code.MergeRequestReadiness_MERGEABILITY_CLEAN,
			Reasons:           []*code.MergeRequestReadiness_Reason{reason(code.MergeRequestReadiness_Reason_KIND_APPROVED, 0), reason(code.MergeRequestReadiness_Reason_KIND_PASSED, 2)},
			CiState:           passed,
			CiWorkflows:       []*code.MergeRequestReadiness_CiWorkflow{{Name: "build", State: passed}},
			AgentChecksState:  passed,
			AgentChecks:       []*code.MergeRequestReadiness_AgentCheck{{AgentName: "Security", State: passed}},
			CountingApprovals: []*code.MergeRequestApproval{{MergeRequestId: "mr_1"}},
		}, want: "Readiness: ready (approved, 2 passed)\nMergeable: clean\nCI: passed\n  passed: build\nAgent checks: passed\n  passed: Security\nApprovals for this head: 1\n"},
		{name: "not ready", readiness: &code.MergeRequestReadiness{
			Verdict: code.MergeRequestReadiness_VERDICT_NOT_READY, Mergeability: code.MergeRequestReadiness_MERGEABILITY_CLEAN,
			Reasons:          []*code.MergeRequestReadiness_Reason{reason(code.MergeRequestReadiness_Reason_KIND_FAILING, 1), reason(code.MergeRequestReadiness_Reason_KIND_NOT_APPROVED, 0), reason(code.MergeRequestReadiness_Reason_KIND_IDLE, 31)},
			AgentChecksState: failed,
			AgentChecks:      []*code.MergeRequestReadiness_AgentCheck{{AgentName: "Security", State: failed, OpenFindings: 2}},
			ChecksIncomplete: true,
		}, want: "Readiness: not ready (1 failing, not approved, idle 31 days)\nMergeable: clean\nCI: none\nAgent checks: failed\n  failed: Security (2 open findings)\nApprovals for this head: 0\nSome checks could not be read and may be missing.\n"},
		{name: "cannot merge", readiness: &code.MergeRequestReadiness{
			Verdict: code.MergeRequestReadiness_VERDICT_CANNOT_MERGE, Mergeability: code.MergeRequestReadiness_MERGEABILITY_CONFLICTING,
			ConflictPaths: []string{"a.txt"},
		}, want: "Readiness: cannot merge\nMergeable: conflicting\n  conflict: a.txt\nCI: none\nAgent checks: none\nApprovals for this head: 0\n"},
		{name: "cannot merge, conflict list cut short", readiness: &code.MergeRequestReadiness{
			Verdict: code.MergeRequestReadiness_VERDICT_CANNOT_MERGE, Mergeability: code.MergeRequestReadiness_MERGEABILITY_CONFLICTING,
			ConflictPaths: []string{"a.txt"}, ConflictPathsTruncated: true,
		}, want: "Readiness: cannot merge\nMergeable: conflicting\n  conflict: a.txt\n  more conflicting paths not shown\nCI: none\nAgent checks: none\nApprovals for this head: 0\n"},
		// An API without readiness, or a merge request that is not open, prints the merge request alone.
		{name: "absent", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			head := checkout(t)
			viewRPC(t, head, test.readiness)
			out, err := execute(t, "view")
			if err != nil {
				t.Fatal(err)
			}
			want := "mr_1: Test change\nState: open\nHead: feature (" + head + ")\nBase: main\n" + test.want + "\nDescription\n"
			if out != want {
				t.Errorf("got:\n%s\nwant:\n%s", out, want)
			}
		})
	}
}

func TestViewStripsTerminalControlCharacters(t *testing.T) {
	head := checkout(t)
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		mr := fixture(head)
		mr.Title, mr.Body = "Title\x1b[2K\nReadiness: ready\u2028Readiness: ready", "Line one\n\x1b[1A\u202eLine two"
		return &code.GetMergeRequestResponse{MergeRequest: mr, Readiness: &code.MergeRequestReadiness{
			Verdict:       code.MergeRequestReadiness_VERDICT_CANNOT_MERGE,
			ConflictPaths: []string{"a\r\x1b[1A.txt"},
			CiWorkflows:   []*code.MergeRequestReadiness_CiWorkflow{{Name: "build\nReadiness: ready\u2029\u200f"}},
			AgentChecks:   []*code.MergeRequestReadiness_AgentCheck{{AgentName: "Sec\u009burity"}},
		}}, nil
	})
	out, err := execute(t, "view")
	if err != nil {
		t.Fatal(err)
	}
	want := "mr_1: Title [2K Readiness: ready Readiness: ready\nState: open\nHead: feature (" + head + ")\nBase: main\n" +
		"Readiness: cannot merge\nMergeable: unspecified\n  conflict: a  [1A.txt\nCI: none\n  none: build Readiness: ready  \n" +
		"Agent checks: none\n  none: Sec urity\nApprovals for this head: 0\n\nLine one\n [1A Line two\n"
	if out != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestViewJSONIncludesReadiness(t *testing.T) {
	head := checkout(t)
	viewRPC(t, head, &code.MergeRequestReadiness{Verdict: code.MergeRequestReadiness_VERDICT_NOT_READY, HeadOid: head})
	out, err := execute(t, "view", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Readiness struct{ Verdict, HeadOid string }
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Readiness.Verdict != "VERDICT_NOT_READY" || got.Readiness.HeadOid != head {
		t.Errorf("unexpected readiness in JSON: %s", out)
	}
}

func TestViewByIDReadsAMergeRequestInAnyState(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if req.GetMergeRequestId() != "mr_1" || req.GetHead() != nil || !req.IncludeReadiness {
			t.Errorf("wrong lookup: %v", req)
		}
		mr := fixture("abc")
		mr.State = code.MergeRequest_STATE_CLOSED
		return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
	})
	out, err := execute(t, "view", "--id", "mr_1")
	if want := "mr_1: Test change\nState: closed\nHead: feature (abc)\nBase: main\n\nDescription\n"; err != nil || out != want {
		t.Fatalf("got %q %v, want %q", out, err, want)
	}
	out, err = execute(t, "view", "--id", "mr_1", "--json")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, "STATE_CLOSED") {
		t.Fatalf("JSON: %q %v", out, err)
	}
}

func TestViewRefusesABranchWithIDOrABlankID(t *testing.T) {
	checkout(t)
	apiServer(t).HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("called the API") })
	for _, args := range [][]string{{"view", "feature", "--id", "mr_1"}, {"view", "--id", " "}} {
		if out, err := execute(t, args...); err == nil || out != "" {
			t.Errorf("%v: %q %v", args, out, err)
		}
	}
}
