package mr

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	"github.com/spf13/cobra"
)

func TestCreateUploadsOrderedImagesAndVideosWithBothFlagNames(t *testing.T) {
	checkout(t)
	for _, path := range []string{"before.png", "demo.mp4"} {
		if err := os.WriteFile(path, []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no MR"))
	})
	var uploaded []string
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUploadMergeRequestAssetProcedure, func(req *code.UploadMergeRequestAssetRequest) (*code.UploadMergeRequestAssetResponse, error) {
		if req.RepositoryId != "repo" || len(req.UploadKey) != 64 {
			t.Fatalf("invalid upload: %v", req)
		}
		uploaded = append(uploaded, string(req.Content))
		id := fmt.Sprintf("mra_%d", len(uploaded))
		return &code.UploadMergeRequestAssetResponse{AssetId: id, Url: "https://depot.dev/api/code/mr-assets/test-org/repo/" + id + map[bool]string{true: ".mp4", false: ".png"}[len(uploaded) == 2]}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(req *code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		want := "Description\n\n![before.png](https://depot.dev/api/code/mr-assets/test-org/repo/mra_1.png)\n\n![demo.mp4](https://depot.dev/api/code/mr-assets/test-org/repo/mra_2.mp4)"
		if req.Body != want || strings.Join(uploaded, ",") != "before.png,demo.mp4" {
			t.Fatalf("unexpected body/order: %s %v", req.Body, uploaded)
		}
		mr := fixture("abc")
		mr.Body = req.Body
		return &code.CreateMergeRequestResponse{MergeRequest: mr}, nil
	})
	out, err := execute(t, "create", "--title", "Screenshots", "--body", "Description", "--image", "before.png", "--attach", "demo.mp4", "--json")
	if err != nil || !strings.Contains(out, "mra_2") {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestFailedImageNeverCreatesMRAndRetryKeepsUploadIdentity(t *testing.T) {
	checkout(t)
	if err := os.WriteFile("asset.png", []byte("asset"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no MR"))
	})
	calls := 0
	var uploadKey string
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUploadMergeRequestAssetProcedure, func(req *code.UploadMergeRequestAssetRequest) (*code.UploadMergeRequestAssetResponse, error) {
		calls++
		if calls == 1 {
			uploadKey = req.UploadKey
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("lost response"))
		}
		if req.UploadKey != uploadKey {
			t.Fatal("retry changed upload identity")
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid asset"))
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(*code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		t.Fatal("created MR with failed upload")
		return nil, nil
	})
	_, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if err == nil || calls != 2 || !strings.Contains(err.Error(), "invalid asset") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestReadImagesBoundsFilesAndRejectsMissingLaterFile(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, size := range []int{0, maxAssetBytes + 1} {
		if err := os.WriteFile("asset.png", bytes.Repeat([]byte{'x'}, size), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readAssets([]string{"asset.png"}); err == nil {
			t.Fatalf("accepted size %d", size)
		}
	}
	if err := os.WriteFile("asset.png", []byte("valid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAssets([]string{"asset.png", "missing.png"}); err == nil {
		t.Fatal("accepted missing asset")
	}
	if _, err := readAssets([]string{"."}); err == nil {
		t.Fatal("accepted directory")
	}
}

func TestRetryOfCreatedMRDoesNotUploadImagesAgain(t *testing.T) {
	head := checkout(t)
	if err := os.WriteFile("asset.png", []byte("asset"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := apiServer(t)
	getRPC(t, mux, fixture(head), "feature")
	out, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if err != nil || !strings.Contains(out, "mr_1") {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestUnsupportedImageUploadDoesNotCreateMR(t *testing.T) {
	checkout(t)
	if err := os.WriteFile("asset.png", []byte("asset"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no MR"))
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUploadMergeRequestAssetProcedure, func(*code.UploadMergeRequestAssetRequest) (*code.UploadMergeRequestAssetResponse, error) {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("upgrade the server"))
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(*code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		t.Fatal("created MR without asset support")
		return nil, nil
	})
	_, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("expected unsupported upload, got %v", err)
	}
}

func TestUploadAcceptsServerResolvedOrganizationAndAppOrigin(t *testing.T) {
	mux := apiServer(t)
	procedure := codev1beta2connect.MergeRequestServiceUploadMergeRequestAssetProcedure
	assetURL := "https://preview.depot.dev/api/code/mr-assets/resolved-org/repo/mra_123.mp4"
	mux.Handle(procedure, connect.NewUnaryHandler(procedure, func(_ context.Context, req *connect.Request[code.UploadMergeRequestAssetRequest]) (*connect.Response[code.UploadMergeRequestAssetResponse], error) {
		if req.Header().Get("X-Depot-Org") != "" || req.Header().Get("Authorization") != "Bearer local-test-token" {
			t.Error("unexpected authentication headers")
		}
		return connect.NewResponse(&code.UploadMergeRequestAssetResponse{AssetId: "mra_123", Url: assetURL}), nil
	}))
	o := &options{repo: "repo", token: "local-test-token"}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetErr(io.Discard)
	body, err := o.uploadAssets(cmd, "head", "title", "Description", []localAsset{{name: "demo.mp4", content: []byte("video")}})
	if err != nil || body != "Description\n\n![demo.mp4]("+assetURL+")" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}
