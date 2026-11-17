package mr

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	gitv1 "github.com/depot/cli/pkg/proto/depot/git/v1"
)

func testOID(s string) *gitv1.ObjectId {
	b, _ := hex.DecodeString(s)
	return &gitv1.ObjectId{Value: &gitv1.ObjectId_Sha1{Sha1: b}}
}

func TestDiffRecordedRevisionsAndPages(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	head, base, ancestor := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	mr := fixture(head)
	mr.BaseOid = base
	mr.BaseRef = "release"
	mr.State = code.MergeRequest_STATE_MERGED
	mr.HeadBranchDeleted = true
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(r *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		if r.GetMergeRequestId() != "mr_1" {
			t.Fatalf("not exact MR: %v", r)
		}
		return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
	})
	calls := 0
	rpc(t, mux, codev1beta2connect.CodeServiceDiffCommitsProcedure, func(r *code.DiffCommitsRequest) (*code.DiffCommitsResponse, error) {
		calls++
		if hex.EncodeToString(r.HeadOid.GetSha1()) != head || hex.EncodeToString(r.Base.GetMergeBaseWithOid().GetSha1()) != base || r.RepositoryId != "repo" {
			t.Errorf("lost historical comparison: %v", r)
		}
		f := &code.DiffCommitsResponse_FileDiff{Path: []byte("new file.txt"), Kind: code.TreeChangeKind_TREE_CHANGE_KIND_ADDED, NewObject: &code.TreeObject{Oid: testOID(head), Mode: code.TreeEntryMode_TREE_ENTRY_MODE_REGULAR_FILE}, Additions: 1, Content: &code.DiffCommitsResponse_FileDiffContent{Kind: &code.DiffCommitsResponse_FileDiffContent_Text_{Text: &code.DiffCommitsResponse_FileDiffContent_Text{Hunks: []*code.DiffCommitsResponse_DiffHunk{{NewStart: 1, NewLines: 1, Lines: []*code.DiffCommitsResponse_DiffLine{{Kind: code.DiffCommitsResponse_DIFF_LINE_KIND_ADDED, Text: []byte("hello"), EndsWithNewline: false}}}}}}}}
		res := &code.DiffCommitsResponse{HeadOid: testOID(head), BaseOid: testOID(ancestor), Files: []*code.DiffCommitsResponse_FileDiff{f}, NextPageToken: "next"}
		if r.PageToken == "next" {
			res.Files = nil
			res.NextPageToken = ""
		}
		return res, nil
	})
	out, err := execute(t, "diff", "--id", "mr_1")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(out, "--- /dev/null\n+++ b/new file.txt\n@@ -0,0 +1,1 @@\n+hello\n\\ No newline at end of file\n") {
		t.Fatalf("incomplete patch (%d calls): %s", calls, out)
	}
	out, err = execute(t, "diff", "--id", "mr_1", "--stat", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `"patch"`) || !strings.Contains(out, `"additions": 1`) {
		t.Fatalf("stat response contains patch or lost counts: %s", out)
	}
	calls = 0
	out, err = execute(t, "diff", "--id", "mr_1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		HeadOid, BaseOid, AnchorBaseOid, NextPageToken string
		Files                                          []struct {
			Path, Patch, ContentKind string
			Additions                *int
		}
	}
	if err = json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || page.HeadOid != head || page.AnchorBaseOid != base || page.BaseOid != ancestor || page.NextPageToken != "next" || len(page.Files) != 1 || page.Files[0].Additions == nil || *page.Files[0].Additions != 1 {
		t.Fatalf("bad JSON page: %s", out)
	}
	out, err = execute(t, "diff", "--id", "mr_1", "--json", "--page-token", page.NextPageToken)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"nextPageToken": ""`) {
		t.Fatal(out)
	}
}

func TestDiffRefusesMissingHistoricalBase(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return &code.GetMergeRequestResponse{MergeRequest: fixture(strings.Repeat("a", 40))}, nil
	})
	for _, cmd := range []string{"diff", "commits", "file"} {
		args := []string{cmd, "--id", "mr_1", "--json"}
		if cmd == "file" {
			args = append(args, "--path", "a.txt")
		}
		out, err := execute(t, args...)
		if err == nil || !strings.Contains(err.Error(), "recorded base") || out != "" {
			t.Fatalf("%s invented a comparison: %s %v", cmd, out, err)
		}
	}
}

func TestCommitsContinuesEmptyPages(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	mr := fixture(head)
	mr.BaseOid = base
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
	})
	message := "Last café commit\x1b[2J\u009b31m\r\b\u202e\u2028\n\nBody"
	calls := 0
	rpc(t, mux, codev1beta2connect.CodeServiceListCommitsProcedure, func(r *code.ListCommitsRequest) (*code.ListCommitsResponse, error) {
		calls++
		if hex.EncodeToString(r.ExcludeReachableFromOid.GetSha1()) != base {
			t.Errorf("wrong exclusion %v", r)
		}
		res := &code.ListCommitsResponse{HeadOid: testOID(head), ExcludeReachableFromOid: testOID(base), NextPageToken: "continue"}
		if r.PageToken != "" {
			res.NextPageToken = ""
			res.Commits = []*code.Commit{{Oid: testOID(head), Message: []byte(message)}}
		}
		return res, nil
	})
	out, err := execute(t, "commits", "--id", "mr_1")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || out != head+" Last café commit [2J 31m    \n" {
		t.Fatalf("lost commits: %s (%d calls)", out, calls)
	}
	out, err = execute(t, "commits", "--id", "mr_1", "--json", "--page-token", "continue")
	if err != nil {
		t.Fatal(err)
	}
	var page commitsJSON
	if err = json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Commits) != 1 || page.Commits[0].Message != message || strings.ContainsRune(out, '\x1b') {
		t.Fatalf("JSON did not preserve the escaped message: %q", out)
	}
}

func TestDiffDoesNotReportWithheldContentsAsEmptyText(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	mr := fixture(head)
	mr.BaseOid = base
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
	})
	rpc(t, mux, codev1beta2connect.CodeServiceDiffCommitsProcedure, func(*code.DiffCommitsRequest) (*code.DiffCommitsResponse, error) {
		files := []*code.DiffCommitsResponse_FileDiff{
			{Path: []byte("image.png"), Kind: code.TreeChangeKind_TREE_CHANGE_KIND_ADDED, NewObject: &code.TreeObject{Oid: testOID(head), Mode: code.TreeEntryMode_TREE_ENTRY_MODE_REGULAR_FILE}, Content: &code.DiffCommitsResponse_FileDiffContent{Kind: &code.DiffCommitsResponse_FileDiffContent_Binary_{Binary: &code.DiffCommitsResponse_FileDiffContent_Binary{}}}},
			{Path: []byte("large.txt"), Kind: code.TreeChangeKind_TREE_CHANGE_KIND_ADDED, NewObject: &code.TreeObject{Oid: testOID(head), Mode: code.TreeEntryMode_TREE_ENTRY_MODE_REGULAR_FILE}, Content: &code.DiffCommitsResponse_FileDiffContent{Kind: &code.DiffCommitsResponse_FileDiffContent_TooLarge_{TooLarge: &code.DiffCommitsResponse_FileDiffContent_TooLarge{NewSize: 2000000}}}},
		}
		return &code.DiffCommitsResponse{HeadOid: testOID(head), BaseOid: testOID(base), Files: files}, nil
	})
	out, err := execute(t, "diff", "--id", "mr_1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var page diffJSON
	if err = json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Files) != 2 || page.Files[0].ContentKind != "binary" || page.Files[1].ContentKind != "too_large" || page.Files[1].Additions != nil || strings.Contains(page.Files[1].Patch, "Binary files") {
		t.Fatal(out)
	}
	if _, err = execute(t, "diff", "--id", "mr_1"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatal("partial text diff reported as complete", err)
	}
}

func TestFileReadsExactChangedPathAcrossPagesAndBlobRanges(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	mr := fixture(head)
	mr.BaseOid = base
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
	})
	pages, reads := 0, 0
	rpc(t, mux, codev1beta2connect.CodeServiceCompareCommitsProcedure, func(r *code.CompareCommitsRequest) (*code.CompareCommitsResponse, error) {
		pages++
		path, token := "notes/child", "next"
		if r.PageToken != "" {
			path = "notes"
			token = ""
		}
		return &code.CompareCommitsResponse{HeadOid: testOID(head), BaseOid: testOID(base), NextPageToken: token, Changes: []*code.CompareCommitsResponse_TreeChange{{Path: []byte(path), Kind: code.TreeChangeKind_TREE_CHANGE_KIND_ADDED, NewObject: &code.TreeObject{Oid: testOID(head), Mode: code.TreeEntryMode_TREE_ENTRY_MODE_REGULAR_FILE}}}}, nil
	})
	content := strings.Repeat("x", 1024*1024) + "\x00\xff"
	rpc(t, mux, codev1beta2connect.CodeServiceGetBlobProcedure, func(r *code.GetBlobRequest) (*code.GetBlobResponse, error) {
		reads++
		start := int(r.Offset)
		end := min(start+int(r.Length), len(content))
		return &code.GetBlobResponse{Size: uint64(len(content)), Content: []byte(content[start:end])}, nil
	})
	out, err := execute(t, "file", "--id", "mr_1", "--path", "notes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var file fileJSON
	if err = json.Unmarshal([]byte(out), &file); err != nil {
		t.Fatal(err)
	}
	if string(file.NewContents) != content || len(file.OldContents) != 0 || file.BaseOid != base || pages != 2 || reads != 2 {
		t.Fatalf("file not read exactly: pages=%d reads=%d bytes=%d", pages, reads, len(file.NewContents))
	}
}

func TestDiffNamesUsesCurrentBranchAndCommitUsesFirstParent(t *testing.T) {
	head := checkout(t)
	mux := apiServer(t)
	mr := fixture(head)
	mr.BaseOid = strings.Repeat("b", 40)
	getRPC(t, mux, mr, "feature")
	selected := strings.Repeat("c", 64)
	rpc(t, mux, codev1beta2connect.CodeServiceCompareCommitsProcedure, func(r *code.CompareCommitsRequest) (*code.CompareCommitsResponse, error) {
		if hexOID(r.HeadOid) != head {
			t.Errorf("wrong MR head %v", r)
		}
		return &code.CompareCommitsResponse{HeadOid: r.HeadOid, BaseOid: testOID(mr.BaseOid), Changes: []*code.CompareCommitsResponse_TreeChange{{Path: []byte("a\nquoted.txt"), Kind: code.TreeChangeKind_TREE_CHANGE_KIND_ADDED, NewObject: &code.TreeObject{Oid: testOID(head), Mode: code.TreeEntryMode_TREE_ENTRY_MODE_REGULAR_FILE}}}}, nil
	})
	out, err := execute(t, "diff", "--name-only")
	if err != nil {
		t.Fatal(err)
	}
	if out != "\"a\\012quoted.txt\"\n" {
		t.Fatalf("unsafe path: %q", out)
	}
	rpc(t, mux, codev1beta2connect.CodeServiceDiffCommitsProcedure, func(r *code.DiffCommitsRequest) (*code.DiffCommitsResponse, error) {
		if hexOID(r.HeadOid) != selected || r.Base.GetFirstParent() == nil {
			t.Errorf("not selected commit/first parent: %v", r)
		}
		return &code.DiffCommitsResponse{HeadOid: r.HeadOid}, nil
	})
	out, err = execute(t, "diff", "--commit", selected, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var page diffJSON
	if err = json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatal(err)
	}
	if page.HeadOid != selected || page.BaseOid != "" || len(page.Files) != 0 {
		t.Fatal(out)
	}
}

func TestFileRefusesOversizedOrIncompleteBlob(t *testing.T) {
	for _, size := range []uint64{maxFileBytes + 1, 12} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			t.Chdir(t.TempDir())
			mux := apiServer(t)
			head := strings.Repeat("a", 40)
			mr := fixture(head)
			mr.BaseOid = strings.Repeat("b", 40)
			rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
				return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
			})
			rpc(t, mux, codev1beta2connect.CodeServiceCompareCommitsProcedure, func(r *code.CompareCommitsRequest) (*code.CompareCommitsResponse, error) {
				return &code.CompareCommitsResponse{HeadOid: r.HeadOid, BaseOid: testOID(mr.BaseOid), Changes: []*code.CompareCommitsResponse_TreeChange{{Path: []byte("a.txt"), Kind: code.TreeChangeKind_TREE_CHANGE_KIND_ADDED, NewObject: &code.TreeObject{Oid: testOID(head), Mode: code.TreeEntryMode_TREE_ENTRY_MODE_REGULAR_FILE}}}}, nil
			})
			rpc(t, mux, codev1beta2connect.CodeServiceGetBlobProcedure, func(*code.GetBlobRequest) (*code.GetBlobResponse, error) {
				return &code.GetBlobResponse{Size: size}, nil
			})
			out, err := execute(t, "file", "--id", "mr_1", "--path", "a.txt", "--json")
			if err == nil || out != "" {
				t.Fatalf("partial blob reported as complete: %s %v", out, err)
			}
		})
	}
}

func TestCommitsJSONRefusesInvalidUTF8(t *testing.T) {
	for _, field := range []string{"message", "author name", "author email"} {
		t.Run(field, func(t *testing.T) {
			t.Chdir(t.TempDir())
			mux := apiServer(t)
			head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
			mr := fixture(head)
			mr.BaseOid = base
			rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
				return &code.GetMergeRequestResponse{MergeRequest: mr}, nil
			})
			commit := &code.Commit{Oid: testOID(head), Message: []byte("Valid"), Author: &code.Commit_CommitIdentity{Name: []byte("René"), Email: []byte("author@example.com")}}
			switch field {
			case "message":
				commit.Message = []byte{0xff}
			case "author name":
				commit.Author.Name = []byte{0xff}
			case "author email":
				commit.Author.Email = []byte{0xff}
			}
			rpc(t, mux, codev1beta2connect.CodeServiceListCommitsProcedure, func(*code.ListCommitsRequest) (*code.ListCommitsResponse, error) {
				return &code.ListCommitsResponse{HeadOid: testOID(head), ExcludeReachableFromOid: testOID(base), Commits: []*code.Commit{commit}}, nil
			})
			out, err := execute(t, "commits", "--id", "mr_1", "--json")
			if err == nil || !strings.Contains(err.Error(), "non-UTF-8") || out != "" {
				t.Fatalf("silently altered %s: output=%q err=%v", field, out, err)
			}
		})
	}
}
