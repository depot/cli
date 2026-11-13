package mr

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
)

func TestCreateUploadsOrderedImagesBeforeOpeningMR(t *testing.T) {
	checkout(t)
	for _, path := range []string{"before.png", "after.png"} {
		if err := os.WriteFile(path, []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no MR"))
	})
	var uploaded []string
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUploadMergeRequestImageProcedure, func(req *code.UploadMergeRequestImageRequest) (*code.UploadMergeRequestImageResponse, error) {
		if req.RepositoryId != "repo" || len(req.UploadKey) != 64 {
			t.Fatalf("invalid upload: %v", req)
		}
		uploaded = append(uploaded, string(req.Content))
		id := fmt.Sprintf("mri_%d", len(uploaded))
		return &code.UploadMergeRequestImageResponse{ImageId: id, Url: "https://depot.dev/api/code/mr-images/test-org/repo/" + id}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(req *code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		want := "Description\n\n![before.png](https://depot.dev/api/code/mr-images/test-org/repo/mri_1)\n\n![after.png](https://depot.dev/api/code/mr-images/test-org/repo/mri_2)"
		if req.Body != want || strings.Join(uploaded, ",") != "before.png,after.png" {
			t.Fatalf("unexpected body/order: %s %v", req.Body, uploaded)
		}
		mr := fixture("abc")
		mr.Body = req.Body
		return &code.CreateMergeRequestResponse{MergeRequest: mr}, nil
	})
	out, err := execute(t, "create", "--title", "Screenshots", "--body", "Description", "--image", "before.png", "--image", "after.png", "--json")
	if err != nil || !strings.Contains(out, "mri_2") {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestFailedImageNeverCreatesMRAndRetryKeepsUploadIdentity(t *testing.T) {
	checkout(t)
	if err := os.WriteFile("image.png", []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no MR"))
	})
	calls := 0
	var uploadKey string
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUploadMergeRequestImageProcedure, func(req *code.UploadMergeRequestImageRequest) (*code.UploadMergeRequestImageResponse, error) {
		calls++
		if calls == 1 {
			uploadKey = req.UploadKey
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("lost response"))
		}
		if req.UploadKey != uploadKey {
			t.Fatal("retry changed upload identity")
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid image"))
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(*code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		t.Fatal("created MR with failed upload")
		return nil, nil
	})
	_, err := execute(t, "create", "--title", "Images", "--image", "image.png")
	if err == nil || calls != 2 || !strings.Contains(err.Error(), "invalid image") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestReadImagesBoundsFilesAndRejectsMissingLaterFile(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, size := range []int{0, maxImageBytes + 1} {
		if err := os.WriteFile("image.png", bytes.Repeat([]byte{'x'}, size), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readImages([]string{"image.png"}); err == nil {
			t.Fatalf("accepted size %d", size)
		}
	}
	if err := os.WriteFile("image.png", []byte("valid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readImages([]string{"image.png", "missing.png"}); err == nil {
		t.Fatal("accepted missing image")
	}
	if _, err := readImages([]string{"."}); err == nil {
		t.Fatal("accepted directory")
	}
}

func TestRetryOfCreatedMRDoesNotUploadImagesAgain(t *testing.T) {
	head := checkout(t)
	if err := os.WriteFile("image.png", []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := apiServer(t)
	getRPC(t, mux, fixture(head), "feature")
	out, err := execute(t, "create", "--title", "Images", "--image", "image.png")
	if err != nil || !strings.Contains(out, "mr_1") {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestUnsupportedImageUploadDoesNotCreateMR(t *testing.T) {
	checkout(t)
	if err := os.WriteFile("image.png", []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no MR"))
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUploadMergeRequestImageProcedure, func(*code.UploadMergeRequestImageRequest) (*code.UploadMergeRequestImageResponse, error) {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("upgrade the server"))
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(*code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		t.Fatal("created MR without image support")
		return nil, nil
	})
	_, err := execute(t, "create", "--title", "Images", "--image", "image.png")
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("expected unsupported upload, got %v", err)
	}
}
