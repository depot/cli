package mr

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	"google.golang.org/protobuf/proto"
)

func TestCommentByIDPreservesBodyAndReviewedAnchorWithoutCheckout(t *testing.T) {
	t.Chdir(t.TempDir())
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if req.GetMergeRequestId() != "mr_1" {
			t.Fatalf("unexpected lookup %v", req)
		}
		return &code.GetMergeRequestResponse{MergeRequest: fixture(strings.Repeat("c", 40))}, nil
	})
	var saved *code.MergeRequestComment
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestCommentProcedure, func(req *code.CreateMergeRequestCommentRequest) (*code.CreateMergeRequestCommentResponse, error) {
		saved = &code.MergeRequestComment{Id: "mrc_1", MergeRequestId: req.MergeRequestId, Body: req.Body, Anchor: req.Anchor}
		returned := proto.Clone(saved).(*code.MergeRequestComment)
		if returned.Anchor == nil {
			returned.Anchor = &code.MergeRequestCommentAnchor{}
		} else {
			returned.Anchor.ProtoReflect().SetUnknown([]byte{0x98, 0x06, 0x01})
		}
		return &code.CreateMergeRequestCommentResponse{Comment: returned}, nil
	})
	for _, side := range []string{"", "old", "new"} {
		args := []string{"comment", "--id", "mr_1", "--body-file", "-", "--json", "--repo", "repo", "--org", "test-org", "--token", "local-test-token"}
		if side != "" {
			args = append(args, "--path", "src/über.go", "--side", side, "--line", "7", "--start-line", "3", "--head-oid", head, "--base-oid", base)
		}
		cmd := NewCmdMR()
		cmd.SetArgs(args)
		cmd.SetIn(strings.NewReader("  Review\n\nThis range.\n"))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if saved.MergeRequestId != "mr_1" || saved.Body != "  Review\n\nThis range.\n" {
			t.Fatalf("lost comment data: %v", saved)
		}
		if side == "" {
			if saved.Anchor != nil {
				t.Fatal("conversation gained an anchor")
			}
			continue
		}
		wantSide := code.MergeRequestCommentAnchor_SIDE_NEW
		if side == "old" {
			wantSide = code.MergeRequestCommentAnchor_SIDE_OLD
		}
		want := &code.MergeRequestCommentAnchor{Path: "src/über.go", Side: wantSide, HeadOid: head, BaseOid: base, StartLine: 3, EndLine: 7}
		if !proto.Equal(saved.Anchor, want) {
			t.Fatalf("reviewed anchor retargeted: %v", saved.Anchor)
		}
	}
}

func TestInlineCommentValidatesBeforeNetworkAndPreservesServerErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	head := strings.Repeat("a", 40)
	good := []string{"comment", "--id", "mr_1", "--body", "Review", "--path", "a.go", "--side", "new", "--line", "3", "--head-oid", head, "--base-oid", ""}
	mux := apiServer(t)
	gets, writes := 0, 0
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		gets++
		return &code.GetMergeRequestResponse{MergeRequest: fixture(head)}, nil
	})
	failure := connect.CodeFailedPrecondition
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestCommentProcedure, func(req *code.CreateMergeRequestCommentRequest) (*code.CreateMergeRequestCommentResponse, error) {
		writes++
		return nil, connect.NewError(failure, errors.New("revision or ownership refused"))
	})
	for _, extra := range [][]string{{"--line", "0"}, {"--start-line", "4"}, {"--side", "left"}, {"--head-oid", "abc"}, {"--base-oid", "abc"}, {"--path", ""}, {"--id", ""}, {"feature"}} {
		if _, err := execute(t, append(append([]string{}, good...), extra...)...); err == nil {
			t.Fatalf("accepted %v", extra)
		}
	}
	if _, err := execute(t, "comment", "--id", "mr_1", "--body", "Review", "--path", "a.go"); err == nil {
		t.Fatal("incomplete anchor accepted")
	}
	if gets != 0 || writes != 0 {
		t.Fatal("invalid comment reached API")
	}
	for _, c := range []connect.Code{connect.CodeFailedPrecondition, connect.CodePermissionDenied, connect.CodeNotFound} {
		failure = c
		if _, err := execute(t, good...); connect.CodeOf(err) != c {
			t.Fatalf("lost API refusal: %v", err)
		}
	}
	if writes != 3 {
		t.Fatalf("retried a failed mutation: %d", writes)
	}
}

func TestCommentEditsAndDeletesRespectMRAndOwnership(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	saved := &code.MergeRequestComment{Id: "mrc_1", MergeRequestId: "mr_1", Body: "Original"}
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return &code.GetMergeRequestResponse{MergeRequest: fixture(strings.Repeat("a", 40)), Comments: []*code.MergeRequestComment{saved}}, nil
	})
	denied := false
	writes := 0
	deleted := false
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUpdateMergeRequestCommentProcedure, func(req *code.UpdateMergeRequestCommentRequest) (*code.UpdateMergeRequestCommentResponse, error) {
		writes++
		if denied {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only the author can edit"))
		}
		if req.CommentId != saved.Id {
			t.Fatal("edited another comment")
		}
		saved.Body = req.Body
		return &code.UpdateMergeRequestCommentResponse{Comment: saved}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceDeleteMergeRequestCommentProcedure, func(req *code.DeleteMergeRequestCommentRequest) (*code.DeleteMergeRequestCommentResponse, error) {
		writes++
		if denied {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only the author can delete"))
		}
		if req.CommentId != saved.Id {
			t.Fatal("deleted another comment")
		}
		deleted = true
		return &code.DeleteMergeRequestCommentResponse{}, nil
	})
	for _, command := range []string{"edit-comment", "delete-comment"} {
		args := []string{command, "mrc_other", "--id", "mr_1"}
		if command == "edit-comment" {
			args = append(args, "--body", "Edited")
		}
		if _, err := execute(t, args...); err == nil {
			t.Fatal("accepted another MR's comment")
		}
	}
	if writes != 0 {
		t.Fatal("wrong MR target mutated")
	}
	if _, err := execute(t, "edit-comment", "mrc_1", "--id", "mr_1", "--body", "Edited\n", "--json"); err != nil {
		t.Fatal(err)
	}
	if saved.Body != "Edited\n" {
		t.Fatal("edit lost body")
	}
	denied = true
	for _, args := range [][]string{{"edit-comment", "mrc_1", "--body", "Forbidden"}, {"delete-comment", "mrc_1"}} {
		if _, err := execute(t, args...); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("lost ownership error: %v", err)
		}
	}
	if deleted || saved.Body != "Edited\n" {
		t.Fatal("unauthorized mutation")
	}
	denied = false
	if _, err := execute(t, "delete-comment", "mrc_1", "--id", "mr_1", "--json"); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("not deleted")
	}
}
