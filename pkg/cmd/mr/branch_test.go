package mr

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var errNoMR = connect.NewError(connect.CodeNotFound, errors.New("Merge request not found"))

func finished(id string, state code.MergeRequest_State, createdMinute int) *code.MergeRequest {
	mr := fixture("abc")
	mr.Id, mr.State = id, state
	mr.CreatedAt = timestamppb.New(time.Date(2026, 1, 1, 0, createdMinute, 0, 0, time.UTC))
	return mr
}

// branchAPI serves lookups the way the API does: an ID finds any merge request, a branch finds the open one, or else
// the newest closed or merged one. Any other call fails the test.
func branchAPI(t *testing.T, mrs ...*code.MergeRequest) *[]*code.GetMergeRequestRequest {
	t.Helper()
	mux := apiServer(t)
	var gets []*code.GetMergeRequestRequest
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		gets = append(gets, req)
		var found *code.MergeRequest
		for _, mr := range mrs {
			if req.GetMergeRequestId() == mr.Id || (req.GetHead().GetHeadRef() == mr.HeadRef && mr.State == code.MergeRequest_STATE_OPEN) {
				return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
			}
			if req.GetHead().GetHeadRef() == mr.HeadRef && (found == nil || mr.CreatedAt.AsTime().After(found.CreatedAt.AsTime())) {
				found = mr
			}
		}
		if found == nil {
			return nil, errNoMR
		}
		return &code.GetMergeRequestResponse{MergeRequest: found}, nil
	})
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("made a call other than a lookup") })
	return &gets
}

func TestBranchLookupFindsTheNewestClosedOrMergedMergeRequest(t *testing.T) {
	checkout(t)
	gets := branchAPI(t, finished("mr_old", code.MergeRequest_STATE_CLOSED, 1), finished("mr_new", code.MergeRequest_STATE_MERGED, 3), finished("mr_mid", code.MergeRequest_STATE_CLOSED, 2))
	out, err := execute(t, "view")
	if want := "mr_new: Test change\nState: merged\nHead: feature (abc)\nBase: main\n\nDescription\n"; err != nil || out != want {
		t.Fatalf("view: %q %v", out, err)
	}
	if got := (*gets)[0]; len(*gets) != 1 || got.GetHead().GetRepositoryId() != "repo" || !got.IncludeReadiness {
		t.Errorf("wrong lookups: %v", *gets)
	}
	if out, err := execute(t, "threads", "feature"); err != nil {
		t.Errorf("threads: %q %v", out, err)
	}

	// Commands that change an open merge request name the one they found and stop.
	for _, args := range [][]string{{"edit", "--title", "New"}, {"close"}, {"approve"}, {"comment", "--body", "Body"}} {
		out, err := execute(t, args...)
		if err == nil || out != "" || !strings.HasPrefix(err.Error(), `merge request mr_new for branch "feature" is merged, not open`) {
			t.Errorf("%v: %q %v", args, out, err)
		}
		if args[0] == "edit" && !strings.HasSuffix(err.Error(), "; pass --id mr_new to edit it anyway") {
			t.Errorf("edit hint: %v", err)
		}
	}
	if _, err := execute(t, "reopen"); err == nil || err.Error() != `merge request mr_new for branch "feature" is merged, not closed` {
		t.Errorf("reopen of a merged merge request: %v", err)
	}
}

func TestBranchLookupPrefersTheOpenMergeRequest(t *testing.T) {
	checkout(t)
	gets := branchAPI(t, finished("mr_new", code.MergeRequest_STATE_CLOSED, 9), fixture("abc"))
	if out, err := execute(t, "view"); err != nil || !strings.HasPrefix(out, "mr_1: ") || len(*gets) != 1 {
		t.Errorf("view: %q %v after %d lookups", out, err, len(*gets))
	}
	if _, err := execute(t, "reopen"); err == nil || err.Error() != `merge request mr_1 for branch "feature" is open, not closed` {
		t.Errorf("reopen of an open merge request: %v", err)
	}
}

func TestReopenByBranchReopensTheNewestClosedMergeRequest(t *testing.T) {
	checkout(t)
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if req.GetHead().GetHeadRef() != "feature" {
			t.Errorf("wrong lookup: %v", req)
		}
		return &code.GetMergeRequestResponse{MergeRequest: finished("mr_7", code.MergeRequest_STATE_CLOSED, 1)}, nil
	})
	updateRPC(t, mux, func(req *code.UpdateMergeRequestRequest) *code.MergeRequest {
		if req.MergeRequestId != "mr_7" || req.State != code.MergeRequest_STATE_OPEN {
			t.Errorf("wrong reopen: %v", req)
		}
		return finished("mr_7", code.MergeRequest_STATE_OPEN, 1)
	})
	if out, err := execute(t, "reopen", "feature"); err != nil || out != "Reopened mr_7: Test change\n" {
		t.Fatalf("reopen: %q %v", out, err)
	}
}

func TestBranchLookupSaysWhenTheBranchHasNoMergeRequest(t *testing.T) {
	checkout(t)
	branchAPI(t)
	for _, args := range [][]string{
		{"view"}, {"view", "feature", "--json"}, {"threads"}, {"edit", "--title", "New"}, {"close"}, {"reopen", "feature"},
		{"approve"}, {"comment", "--body", "Body"},
	} {
		out, err := execute(t, args...)
		want := `no merge request found for branch "feature" in repository repo: not_found: Merge request not found`
		if connect.CodeOf(err) != connect.CodeNotFound || out != "" || err.Error() != want {
			t.Errorf("%v: %q %v", args, out, err)
		}
	}
}

// A branch whose merge requests are all closed or merged gets a new one: create never reuses or edits those.
func TestCreateOpensANewMergeRequestWhenTheBranchHasOnlyFinishedOnes(t *testing.T) {
	for _, state := range []code.MergeRequest_State{code.MergeRequest_STATE_CLOSED, code.MergeRequest_STATE_MERGED} {
		for _, attach := range []bool{false, true} {
			checkout(t)
			writeAsset(t, "asset.png", pngMagic+"asset")
			mux := apiServer(t)
			getRPC(t, mux, finished("mr_old", state, 1), "feature")
			newAssetAPI(t, mux)
			created := createRPC(t, mux)
			rpc(t, mux, codev1beta2connect.MergeRequestServiceUpdateMergeRequestProcedure, func(*code.UpdateMergeRequestRequest) (*code.UpdateMergeRequestResponse, error) {
				t.Error("edited the finished merge request")
				return nil, errNoMR
			})
			args := []string{"create", "--title", "Again", "--body", "Body"}
			if attach {
				args = append(args, "--attach", "asset.png")
			}
			out, err := execute(t, args...)
			if err != nil || *created != 1 || strings.Contains(out, "mr_old") {
				t.Errorf("%v attach=%v: created=%d out=%q err=%v", state, attach, *created, out, err)
			}
		}
	}
}

func TestBranchLookupKeepsOtherAPIErrors(t *testing.T) {
	checkout(t)
	failure := connect.NewError(connect.CodePermissionDenied, errors.New("denied"))
	rpc(t, apiServer(t), codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return nil, failure
	})
	for _, args := range [][]string{{"view"}, {"close"}, {"approve"}} {
		if _, err := execute(t, args...); err == nil || err.Error() != failure.Error() {
			t.Errorf("%v: got %v, want %v", args, err, failure)
		}
	}
	// The server names a wrong repository; the message keeps that.
	failure = connect.NewError(connect.CodeNotFound, errors.New("Repository not found"))
	if _, err := execute(t, "view"); err == nil || !strings.HasSuffix(err.Error(), "in repository repo: not_found: Repository not found") {
		t.Errorf("wrong repository: %v", err)
	}
}
