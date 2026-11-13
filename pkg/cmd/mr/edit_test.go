package mr

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
)

func executeIn(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := NewCmdMR()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append(args, "--repo", "repo", "--org", "test-org", "--token", "local-test-token"))
	err := cmd.Execute()
	return out.String(), err
}

func bodyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func updateRPC(t *testing.T, mux *http.ServeMux, f func(*code.UpdateMergeRequestRequest) *code.MergeRequest) {
	t.Helper()
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUpdateMergeRequestProcedure, func(req *code.UpdateMergeRequestRequest) (*code.UpdateMergeRequestResponse, error) {
		return &code.UpdateMergeRequestResponse{MergeRequest: f(req)}, nil
	})
}

func TestCreateWithBaseUsesTheBaseRPC(t *testing.T) {
	checkout(t)
	mux := apiServer(t)
	existingBase := "parent"
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestWithBaseProcedure, func(req *code.CreateMergeRequestWithBaseRequest) (*code.CreateMergeRequestWithBaseResponse, error) {
		if req.RepositoryId != "repo" || req.HeadRef != "feature" || req.Title != "Stacked" || req.Body != "Body" || req.GetBaseRef() != "parent" {
			t.Errorf("wrong creation request: %v", req)
		}
		mr := fixture("abc")
		mr.BaseRef = existingBase
		return &code.CreateMergeRequestWithBaseResponse{MergeRequest: mr}, nil
	})
	args := []string{"create", "--title", "Stacked", "--body", "Body", "--base", "parent"}
	out, err := execute(t, args...)
	if err != nil || !strings.Contains(out, "Base: parent") {
		t.Fatalf("create with base: %q %v", out, err)
	}
	out, err = execute(t, append(args, "--json")...)
	// protojson varies its whitespace between runs, so compare without it.
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(strings.ReplaceAll(out, " ", ""), `"baseRef":"parent"`) {
		t.Fatalf("create with base JSON: %q %v", out, err)
	}
	// The API returns an existing open merge request unchanged, whatever base was asked for.
	existingBase = "main"
	out, err = execute(t, args...)
	if err == nil || out != "" || !strings.Contains(err.Error(), "depot mr edit feature --base parent") {
		t.Fatalf("existing merge request with another base should be refused: %q %v", out, err)
	}
}

func TestBodyFileIsSentUnchangedFromFileAndStdin(t *testing.T) {
	head := checkout(t)
	mux := apiServer(t)
	const content = "# Heading\n\nSecond paragraph with \"quotes\".\n"
	var bodies []string
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(req *code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		bodies = append(bodies, req.Body)
		return &code.CreateMergeRequestResponse{MergeRequest: fixture(head)}, nil
	})
	getRPC(t, mux, fixture(head), "feature")
	updateRPC(t, mux, func(req *code.UpdateMergeRequestRequest) *code.MergeRequest {
		if req.Title != nil || req.BaseRef != nil || req.State != code.MergeRequest_STATE_UNSPECIFIED {
			t.Errorf("body-only edit changed other fields: %v", req)
		}
		bodies = append(bodies, req.GetBody())
		return fixture(head)
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestCommentProcedure, func(req *code.CreateMergeRequestCommentRequest) (*code.CreateMergeRequestCommentResponse, error) {
		bodies = append(bodies, req.Body)
		return &code.CreateMergeRequestCommentResponse{Comment: &code.MergeRequestComment{Id: "mrc_1", MergeRequestId: req.MergeRequestId}}, nil
	})
	path := bodyFile(t, content)
	for _, command := range [][]string{{"create", "--title", "Title"}, {"edit"}, {"comment"}} {
		if _, err := executeIn(t, "", append(command, "--body-file", path)...); err != nil {
			t.Fatalf("%v from file: %v", command, err)
		}
		if _, err := executeIn(t, content, append(command, "--body-file", "-")...); err != nil {
			t.Fatalf("%v from stdin: %v", command, err)
		}
	}
	if len(bodies) != 6 {
		t.Fatalf("expected 6 bodies, got %d", len(bodies))
	}
	for i, body := range bodies {
		if body != content {
			t.Errorf("body %d was changed: %q", i, body)
		}
	}
}

func TestEditSendsOnlyPassedFieldsByBranchAndByID(t *testing.T) {
	head := checkout(t)
	mux := apiServer(t)
	lookups := 0
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if req.GetHead().GetHeadRef() != "other" {
			t.Errorf("wrong branch lookup: %v", req)
		}
		lookups++
		return &code.GetMergeRequestResponse{MergeRequest: fixture(head)}, nil
	})
	var got *code.UpdateMergeRequestRequest
	updateRPC(t, mux, func(req *code.UpdateMergeRequestRequest) *code.MergeRequest {
		got = req
		mr := fixture(head)
		mr.Title, mr.BaseRef = "New title", "parent"
		return mr
	})
	out, err := execute(t, "edit", "other", "--title", "New title", "--base", "parent")
	if err != nil || !strings.Contains(out, "mr_1: New title") || !strings.Contains(out, "Base: parent") {
		t.Fatalf("edit by branch: %q %v", out, err)
	}
	if got.MergeRequestId != "mr_1" || got.GetTitle() != "New title" || got.GetBaseRef() != "parent" || got.Body != nil || got.State != code.MergeRequest_STATE_UNSPECIFIED {
		t.Errorf("wrong update: %v", got)
	}
	out, err = execute(t, "edit", "--id", "mr_9", "--body", "", "--json")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("edit by ID: %q %v", out, err)
	}
	if got.MergeRequestId != "mr_9" || got.Title != nil || got.BaseRef != nil || got.Body == nil || *got.Body != "" {
		t.Errorf("clearing the body by ID sent: %v", got)
	}
	if lookups != 1 {
		t.Errorf("expected a lookup only for the branch form, got %d", lookups)
	}
}

func TestCloseAndReopenSetState(t *testing.T) {
	head := checkout(t)
	mux := apiServer(t)
	open := true
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if !open {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("Merge request not found"))
		}
		return &code.GetMergeRequestResponse{MergeRequest: fixture(head)}, nil
	})
	updateRPC(t, mux, func(req *code.UpdateMergeRequestRequest) *code.MergeRequest {
		if req.MergeRequestId != "mr_1" || req.Title != nil || req.Body != nil || req.BaseRef != nil {
			t.Errorf("wrong state change: %v", req)
		}
		open = req.State == code.MergeRequest_STATE_OPEN
		mr := fixture(head)
		mr.State = req.State
		return mr
	})
	out, err := execute(t, "close")
	if err != nil || out != "Closed mr_1: Test change\n" || open {
		t.Fatalf("close: %q %v", out, err)
	}
	out, err = execute(t, "reopen", "feature")
	if connect.CodeOf(err) != connect.CodeNotFound || out != "" || !strings.Contains(err.Error(), "--id") {
		t.Fatalf("reopen by branch should point at --id: %q %v", out, err)
	}
	out, err = execute(t, "reopen", "--id", "mr_1")
	if err != nil || out != "Reopened mr_1: Test change\n" || !open {
		t.Fatalf("reopen: %q %v", out, err)
	}
	out, err = execute(t, "close", "--id", "mr_1", "--json")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, "STATE_CLOSED") {
		t.Fatalf("close JSON: %q %v", out, err)
	}
}

func TestLocalEditValidationMakesNoAPICall(t *testing.T) {
	checkout(t)
	mux := apiServer(t)
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { t.Error("unexpected API request") })
	missing := filepath.Join(t.TempDir(), "missing.md")
	binary := bodyFile(t, "\xff\xfe")
	for _, args := range [][]string{
		{"edit"}, {"edit", "--title", " "}, {"edit", "--base", ""}, {"edit", "--base", "-x"},
		{"edit", "feature", "--id", "mr_1", "--title", "T"}, {"edit", "--id", " ", "--title", "T"},
		{"edit", "--body", "a", "--body-file", missing}, {"edit", "--body-file", missing}, {"edit", "--body-file", binary},
		{"create", "--title", "T", "--base", ""}, {"create", "--title", "T", "--body-file", missing},
		{"comment", "--body-file", missing}, {"comment", "--body-file", "-"},
		{"close", "feature", "--id", "mr_1"}, {"reopen", "-x"},
	} {
		if out, err := executeIn(t, " \n", args...); err == nil || out != "" {
			t.Errorf("%v: expected local refusal, got %q %v", args, out, err)
		}
	}
}
