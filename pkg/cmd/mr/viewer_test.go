package mr

import (
	"connectrpc.com/connect"
	"encoding/json"
	"errors"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	"strings"
	"testing"
)

func TestViewerOutsideCheckout(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetViewerProcedure, func(_ *code.GetViewerRequest) (*code.GetViewerResponse, error) {
		return &code.GetViewerResponse{OrgId: "test-org", Viewer: &code.MergeRequestActor{Kind: code.MergeRequestActor_KIND_USER, Id: "user_1", DisplayName: "Person\x1b[31m"}}, nil
	})
	out, err := executeWithoutRepo(t, "viewer", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		OrgID  string `json:"orgId"`
		Viewer struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"viewer"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		t.Fatal(err)
	}
	if data.OrgID != "test-org" || data.Viewer.ID != "user_1" || data.Viewer.DisplayName != "Person\x1b[31m" {
		t.Fatalf("unexpected viewer: %s", out)
	}
	out, err = executeWithoutRepo(t, "viewer")
	if err != nil || strings.ContainsRune(out, '\x1b') || !strings.Contains(out, "user_1") {
		t.Fatalf("unsafe or missing viewer: %q, %v", out, err)
	}
}

func TestViewerRefusesUnavailableOrMismatchedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *code.GetViewerResponse
		err      error
		want     string
	}{
		{name: "old router", err: connect.NewError(connect.CodeUnknown, errors.New("Route POST:/depot.code.v1beta2.MergeRequestService/GetViewer not found")), want: "does not support viewer"},
		{name: "old API", err: connect.NewError(connect.CodeUnimplemented, nil), want: "does not support viewer"},
		{name: "missing viewer", response: &code.GetViewerResponse{}, want: "no authenticated user"},
		{name: "wrong organization", response: &code.GetViewerResponse{OrgId: "another", Viewer: &code.MergeRequestActor{Id: "user", Kind: code.MergeRequestActor_KIND_USER}}, want: "another organization"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc(t, apiServer(t), codev1beta2connect.MergeRequestServiceGetViewerProcedure, func(_ *code.GetViewerRequest) (*code.GetViewerResponse, error) {
				return tc.response, tc.err
			})
			out, err := execute(t, "viewer", "--json")
			if err == nil || !strings.Contains(err.Error(), tc.want) || out != "" {
				t.Fatalf("got %q, %v", out, err)
			}
		})
	}
}

func TestViewPreservesViewerPermissions(t *testing.T) {
	rpc(t, apiServer(t), codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(_ *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return &code.GetMergeRequestResponse{MergeRequest: fixture(strings.Repeat("a", 40)), Viewer: &code.MergeRequestActor{Id: "user", Kind: code.MergeRequestActor_KIND_USER}, ViewerPermissions: &code.MergeRequestViewerPermissions{CanComment: true}}, nil
	})
	out, err := execute(t, "view", "--id", "mr_1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
		Permissions struct {
			Comment bool `json:"canComment"`
			Merge   bool `json:"canMerge"`
		} `json:"viewerPermissions"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		t.Fatal(err)
	}
	if data.Viewer.ID != "user" || !data.Permissions.Comment || data.Permissions.Merge {
		t.Fatalf("wrong permissions: %s", out)
	}
}
