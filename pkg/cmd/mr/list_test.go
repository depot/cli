package mr

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	"google.golang.org/protobuf/proto"
)

func listRPC(t *testing.T, resp *code.ListMergeRequestsResponse) *[]*code.ListMergeRequestsRequest {
	t.Helper()
	var requests []*code.ListMergeRequestsRequest
	rpc(t, apiServer(t), codev1beta2connect.MergeRequestServiceListMergeRequestsProcedure, func(req *code.ListMergeRequestsRequest) (*code.ListMergeRequestsResponse, error) {
		requests = append(requests, req)
		return resp, nil
	})
	return &requests
}

func TestListSendsFiltersAndPrintsOneLinePerMR(t *testing.T) {
	merged := fixture("abc")
	merged.State = code.MergeRequest_STATE_MERGED
	merged.Title = "Test\nchange\u202e\u2028"
	requests := listRPC(t, &code.ListMergeRequestsResponse{MergeRequests: []*code.MergeRequest{merged}})

	out, err := execute(t, "list", "--state", "merged", "--head", "feature", "--base", "main", "--author", "@me", "--limit", "5", "--page-token", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if out != "mr_1\tmerged\tfeature -> main\tTest change  \n" {
		t.Errorf("unexpected output %q", out)
	}
	want := &code.ListMergeRequestsRequest{
		RepositoryId: "repo", State: code.ListMergeRequestsRequest_STATE_FILTER_MERGED,
		HeadRef: "feature", BaseRef: "main", AuthoredByMe: true, PageSize: 5, PageToken: "tok",
	}
	if len(*requests) != 1 || !proto.Equal((*requests)[0], want) {
		t.Errorf("unexpected request %v", *requests)
	}
}

func TestListDefaultsToOpenAndMapsAuthors(t *testing.T) {
	requests := listRPC(t, &code.ListMergeRequestsResponse{})
	for _, author := range []string{"", "user-1", "agent:agent-1"} {
		out, err := execute(t, "list", "--author", author)
		if err != nil {
			t.Fatal(err)
		}
		if out != "" {
			t.Errorf("expected empty stdout, got %q", out)
		}
	}
	wantAuthors := []*code.MergeRequestActor{
		nil,
		{Kind: code.MergeRequestActor_KIND_USER, Id: "user-1"},
		{Kind: code.MergeRequestActor_KIND_AGENT, Id: "agent-1"},
	}
	for i, req := range *requests {
		if req.State != code.ListMergeRequestsRequest_STATE_FILTER_OPEN || req.AuthoredByMe || !proto.Equal(req.Author, wantAuthors[i]) {
			t.Errorf("request %d: unexpected %v", i, req)
		}
	}
}

func TestListJSONKeepsNextPageToken(t *testing.T) {
	listRPC(t, &code.ListMergeRequestsResponse{MergeRequests: []*code.MergeRequest{fixture("abc")}, NextPageToken: "next"})
	out, err := execute(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		MergeRequests []struct{ ID string }
		NextPageToken string
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.MergeRequests) != 1 || response.MergeRequests[0].ID != "mr_1" || response.NextPageToken != "next" {
		t.Fatalf("unexpected JSON: %s", out)
	}
}

func TestListPrintsNextPageHintToStderr(t *testing.T) {
	resp := &code.ListMergeRequestsResponse{MergeRequests: []*code.MergeRequest{fixture("abc")}, NextPageToken: "next"}
	listRPC(t, resp)
	for _, want := range []struct{ stdout, notice string }{{"mr_1\topen\tfeature -> main\tTest change\n", ""}, {"", "No merge requests found\n"}} {
		cmd := NewCmdMR()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs([]string{"list", "--repo", "repo", "--org", "test-org", "--token", "local-test-token"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if out.String() != want.stdout || errOut.String() != want.notice+"More merge requests: rerun with --page-token next\n" {
			t.Errorf("unexpected stdout %q, stderr %q", out.String(), errOut.String())
		}
		// The second pass checks an empty page still carries the token.
		resp.MergeRequests = nil
	}
}

func TestListRejectsBadFlagsWithoutAPICall(t *testing.T) {
	requests := listRPC(t, &code.ListMergeRequestsResponse{})
	for _, args := range [][]string{{"--state", "draft"}, {"--author", "agent:"}} {
		_, err := execute(t, append([]string{"list"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "invalid --") {
			t.Errorf("%v: expected a flag error, got %v", args, err)
		}
	}
	if len(*requests) != 0 {
		t.Errorf("expected no API calls, got %d", len(*requests))
	}
}
