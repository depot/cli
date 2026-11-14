package mr

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// threadsRPC serves a merge request with a resolved thread on the current diff, an open thread from an older
// head that has a reply, and one conversation comment.
func threadsRPC(t *testing.T, mux *http.ServeMux, head string) {
	t.Helper()
	user := func(id string) *code.MergeRequestActor {
		return &code.MergeRequestActor{Kind: code.MergeRequestActor_KIND_USER, Id: id}
	}
	anchor := func(headOid, path string, start, end uint32) *code.MergeRequestCommentAnchor {
		return &code.MergeRequestCommentAnchor{HeadOid: headOid, Path: path, Side: code.MergeRequestCommentAnchor_SIDE_NEW, StartLine: start, EndLine: end}
	}
	at := timestamppb.New(timestamppb.Now().AsTime())
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if req.GetMergeRequestId() != "mr_1" && req.GetHead().GetHeadRef() != "feature" {
			t.Errorf("wrong lookup: %v", req)
		}
		return &code.GetMergeRequestResponse{MergeRequest: fixture(head), Comments: []*code.MergeRequestComment{
			{Id: "mrc_done", MergeRequestId: "mr_1", Author: user("ann"), Body: "Rename this", CreatedAt: at, Anchor: anchor(head, "a.go", 3, 3), ResolvedAt: at, ResolvedBy: user("bob")},
			{Id: "mrc_talk", MergeRequestId: "mr_1", Author: user("ann"), Body: "Nice work", CreatedAt: at},
			{Id: "mrc_open", MergeRequestId: "mr_1", Author: user("bob"), Body: "Why \x1b[31mthis?", CreatedAt: at, Anchor: anchor("old", "b.go", 7, 9)},
			{Id: "mrc_reply", MergeRequestId: "mr_1", Author: user("ann"), Body: "Because", CreatedAt: at, InReplyToId: "mrc_open"},
		}}, nil
	})
}

func TestThreadsGroupsRepliesAndMarksResolvedAndOutdated(t *testing.T) {
	head := checkout(t)
	threadsRPC(t, apiServer(t), head)
	out, err := execute(t, "threads")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Thread mrc_done  a.go:3  resolved\n", "Thread mrc_open  b.go:7-9  unresolved, outdated\n",
		"  mrc_reply  user ann  ", "    Why  [31mthis?\n", "Conversation\n  mrc_talk  user ann",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "mrc_open") > strings.Index(out, "mrc_reply") || strings.Index(out, "mrc_reply") > strings.Index(out, "Conversation") {
		t.Errorf("reply is not under its thread:\n%s", out)
	}
	out, err = execute(t, "threads", "--id", "mr_1", "--unresolved")
	if err != nil || strings.Contains(out, "mrc_done") || strings.Contains(out, "mrc_talk") || !strings.Contains(out, "mrc_reply") {
		t.Fatalf("--unresolved: %s %v", out, err)
	}
}

func TestThreadsJSONListsThreadsWithTheirComments(t *testing.T) {
	head := checkout(t)
	threadsRPC(t, apiServer(t), head)
	type listing struct {
		MergeRequestID string `json:"mergeRequestId"`
		Threads        []struct {
			ID, Path, Side     string
			StartLine, EndLine uint32
			Resolved, Outdated bool
			Comments           []struct {
				ID          string
				InReplyToID string `json:"inReplyToId"`
				Author      struct{ ID string }
			}
		}
		Comments []struct{ ID string }
	}
	var all, open listing
	for args, into := range map[string]*listing{"": &all, "--unresolved": &open} {
		out, err := execute(t, strings.Fields("threads --json "+args)...)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(out), into); err != nil {
			t.Fatalf("%v:\n%s", err, out)
		}
	}
	if all.MergeRequestID != "mr_1" || len(all.Threads) != 2 || len(all.Comments) != 1 || all.Comments[0].ID != "mrc_talk" {
		t.Fatalf("wrong listing: %+v", all)
	}
	if done := all.Threads[0]; done.ID != "mrc_done" || !done.Resolved || done.Outdated || done.Path != "a.go" || done.StartLine != 3 {
		t.Errorf("wrong resolved thread: %+v", done)
	}
	if len(open.Threads) != 1 || len(open.Comments) != 0 {
		t.Fatalf("wrong unresolved listing: %+v", open)
	}
	thread := open.Threads[0]
	if thread.ID != "mrc_open" || thread.Resolved || !thread.Outdated || thread.Side != "new" || thread.EndLine != 9 {
		t.Errorf("wrong open thread: %+v", thread)
	}
	if len(thread.Comments) != 2 || thread.Comments[0].Author.ID != "bob" || thread.Comments[1].InReplyToID != "mrc_open" {
		t.Errorf("wrong thread comments: %+v", thread.Comments)
	}
}

func TestReplyResolveAndUnresolveNameOnlyTheComment(t *testing.T) {
	mux := apiServer(t)
	root := func(resolved bool) *code.MergeRequestComment {
		comment := &code.MergeRequestComment{Id: "mrc_open", MergeRequestId: "mr_1"}
		if resolved {
			comment.ResolvedAt = timestamppb.Now()
		}
		return comment
	}
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestCommentProcedure, func(req *code.CreateMergeRequestCommentRequest) (*code.CreateMergeRequestCommentResponse, error) {
		if req.MergeRequestId != "" || req.InReplyToId != "mrc_reply" || req.Body != "Done" || req.Anchor != nil {
			t.Errorf("wrong reply: %v", req)
		}
		return &code.CreateMergeRequestCommentResponse{Comment: &code.MergeRequestComment{Id: "mrc_new", MergeRequestId: "mr_1", InReplyToId: "mrc_open"}}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceResolveMergeRequestThreadProcedure, func(req *code.ResolveMergeRequestThreadRequest) (*code.ResolveMergeRequestThreadResponse, error) {
		if req.CommentId != "mrc_reply" {
			t.Errorf("wrong comment %q", req.CommentId)
		}
		return &code.ResolveMergeRequestThreadResponse{Comment: root(true)}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUnresolveMergeRequestThreadProcedure, func(req *code.UnresolveMergeRequestThreadRequest) (*code.UnresolveMergeRequestThreadResponse, error) {
		if req.CommentId != "mrc_reply" {
			t.Errorf("wrong comment %q", req.CommentId)
		}
		return &code.UnresolveMergeRequestThreadResponse{Comment: root(false)}, nil
	})
	for args, want := range map[string]string{
		"reply mrc_reply --body Done": "Replied in thread mrc_open on mr_1: mrc_new\n",
		"resolve mrc_reply":           "Resolved thread mrc_open on mr_1\n",
		"unresolve mrc_reply":         "Unresolved thread mrc_open on mr_1\n",
	} {
		// These run outside a checkout and without --repo: a comment ID is enough.
		cmd := NewCmdMR()
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append(strings.Fields(args), "--org", "test-org", "--token", "local-test-token"))
		if err := cmd.Execute(); err != nil || out.String() != want {
			t.Errorf("%s: %q %v", args, out.String(), err)
		}
		text, err := execute(t, append(strings.Fields(args), "--json")...)
		if err != nil || !json.Valid([]byte(text)) || !strings.Contains(text, `"comment"`) {
			t.Errorf("%s --json: %s %v", args, text, err)
		}
	}
}

func TestThreadCommandsExplainAnAPIWithoutThreads(t *testing.T) {
	mux := apiServer(t)
	// An API from before threads has no resolve routes and reads a reply as a comment that names no merge request.
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestCommentProcedure, func(*code.CreateMergeRequestCommentRequest) (*code.CreateMergeRequestCommentResponse, error) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("merge_request_id is required"))
	})
	for _, args := range []string{"reply mrc_1 --body Done", "resolve mrc_1", "unresolve mrc_1"} {
		out, err := execute(t, strings.Fields(args)...)
		if err == nil || out != "" || !strings.Contains(err.Error(), "does not support review threads yet") {
			t.Errorf("%s: %q %v", args, out, err)
		}
	}
}

func TestThreadCommandsValidateLocallyAndKeepAPIRefusals(t *testing.T) {
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceResolveMergeRequestThreadProcedure, func(*code.ResolveMergeRequestThreadRequest) (*code.ResolveMergeRequestThreadResponse, error) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Only comments on lines of the diff form a review thread"))
	})
	if _, err := execute(t, "resolve", "mrc_talk"); connect.CodeOf(err) != connect.CodeFailedPrecondition || strings.Contains(err.Error(), "does not support") {
		t.Errorf("refusal was not passed through: %v", err)
	}
	mux.HandleFunc("/depot.code.v1beta2.MergeRequestService/CreateMergeRequestComment", func(http.ResponseWriter, *http.Request) {
		t.Error("called the API after a local validation failure")
	})
	for _, args := range [][]string{{"reply", "mrc_1"}, {"reply", "mrc_1", "--body", " "}, {"reply", " ", "--body", "x"}, {"resolve"}, {"unresolve", " "}, {"threads", "feature", "--id", "mr_1"}} {
		if out, err := execute(t, args...); err == nil || out != "" {
			t.Errorf("%v: %q %v", args, out, err)
		}
	}
}

func TestThreadOnAMergeRequestWithoutARecordedBaseIsNotOutdated(t *testing.T) {
	anchor := &code.MergeRequestCommentAnchor{HeadOid: "h", BaseOid: "b"}
	for _, c := range []struct {
		head, base string
		want       bool
	}{{"h", "", false}, {"h", "b", false}, {"h", "moved", true}, {"moved", "", true}} {
		if got := outdated(anchor, &code.MergeRequest{HeadOid: c.head, BaseOid: c.base}); got != c.want {
			t.Errorf("head %q base %q: outdated = %v, want %v", c.head, c.base, got, c.want)
		}
	}
}
