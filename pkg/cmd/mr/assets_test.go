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

// Asset fixtures only need a supported signature; the API does the real decoding.
const pngMagic = "\x89PNG\r\n\x1a\n"

func TestCreateUploadsOrderedImagesAndVideosWithBothFlagNames(t *testing.T) {
	checkout(t)
	for _, path := range []string{"before.png", "demo.mp4"} {
		if err := os.WriteFile(path, []byte(pngMagic+path), 0600); err != nil {
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
		if req.Body != want || strings.Join(uploaded, ",") != pngMagic+"before.png,"+pngMagic+"demo.mp4" {
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
	if err := os.WriteFile("asset.png", []byte(pngMagic+"asset"), 0600); err != nil {
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
	if err := os.WriteFile("asset.png", []byte(pngMagic+"valid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAssets([]string{"asset.png", "missing.png"}); err == nil {
		t.Fatal("accepted missing asset")
	}
	if _, err := readAssets([]string{"."}); err == nil {
		t.Fatal("accepted directory")
	}
}

func TestCreateAttachesToExistingMRDescription(t *testing.T) {
	head := checkout(t)
	if err := os.WriteFile("asset.png", []byte(pngMagic+"asset"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := apiServer(t)
	existing := fixture(head)
	getRPC(t, mux, existing, "feature")
	assetURL := "https://depot.dev/api/code/mr-assets/test-org/repo/mra_1.png"
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUploadMergeRequestAssetProcedure, func(*code.UploadMergeRequestAssetRequest) (*code.UploadMergeRequestAssetResponse, error) {
		// Someone edits the description while the upload is in flight.
		existing.Body = "Edited meanwhile"
		return &code.UploadMergeRequestAssetResponse{AssetId: "mra_1", Url: assetURL}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(*code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		t.Error("created a second MR")
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unexpected create"))
	})
	updates := 0
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUpdateMergeRequestProcedure, func(req *code.UpdateMergeRequestRequest) (*code.UpdateMergeRequestResponse, error) {
		updates++
		want := "Edited meanwhile\n\n![asset.png](" + assetURL + ")"
		if req.MergeRequestId != "mr_1" || req.GetBody() != want || req.Title != nil || req.BaseRef != nil || req.State != code.MergeRequest_STATE_UNSPECIFIED {
			t.Errorf("unexpected update: %v", req)
		}
		mr := fixture(head)
		mr.Body = req.GetBody()
		return &code.UpdateMergeRequestResponse{MergeRequest: mr}, nil
	})
	out, err := execute(t, "create", "--title", "Ignored", "--body", "Ignored", "--attach", "asset.png")
	if err != nil || updates != 1 || !strings.Contains(out, "mr_1: Test change") || !strings.Contains(out, assetURL) {
		t.Fatalf("updates=%d out=%s err=%v", updates, out, err)
	}
}

func TestCreateFailsWhenMROpenedDuringUploadLacksAttachments(t *testing.T) {
	head := checkout(t)
	if err := os.WriteFile("asset.png", []byte(pngMagic+"asset"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no MR"))
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUploadMergeRequestAssetProcedure, func(*code.UploadMergeRequestAssetRequest) (*code.UploadMergeRequestAssetResponse, error) {
		return &code.UploadMergeRequestAssetResponse{AssetId: "mra_1", Url: "https://depot.dev/api/code/mr-assets/test-org/repo/mra_1.png"}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(*code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		return &code.CreateMergeRequestResponse{MergeRequest: fixture(head)}, nil
	})
	out, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if err == nil || out != "" || !strings.Contains(err.Error(), "mr_1 was opened for this branch during the upload") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestFailedUploadLeavesExistingMRUnchanged(t *testing.T) {
	head := checkout(t)
	if err := os.WriteFile("asset.png", []byte(pngMagic+"asset"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := apiServer(t)
	getRPC(t, mux, fixture(head), "feature")
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUploadMergeRequestAssetProcedure, func(*code.UploadMergeRequestAssetRequest) (*code.UploadMergeRequestAssetResponse, error) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid asset"))
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUpdateMergeRequestProcedure, func(*code.UpdateMergeRequestRequest) (*code.UpdateMergeRequestResponse, error) {
		t.Error("updated MR after a failed upload")
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unexpected update"))
	})
	_, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if err == nil || !strings.Contains(err.Error(), "invalid asset") || !strings.Contains(err.Error(), "mr_1 was not changed") {
		t.Fatalf("err=%v", err)
	}
}

func TestReadAssetsRejectsUnsupportedContent(t *testing.T) {
	t.Chdir(t.TempDir())
	accepted := []string{pngMagic, "\xff\xd8\xff\xe0", "RIFF\x00\x00\x00\x00WEBPVP8 ", "\x00\x00\x00\x18ftypmp42", "\x00\x00\x00\x14ftypqt  ", "\x00\x00\x00\x08wide", "\x1a\x45\xdf\xa3"}
	for _, content := range accepted {
		if err := os.WriteFile("asset", []byte(content+"rest of file"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readAssets([]string{"asset"}); err != nil {
			t.Errorf("rejected %q: %v", content, err)
		}
	}
	for _, content := range []string{"plain text notes", "GIF89a", "%PDF-1.7", "RIFF\x00\x00\x00\x00WAVEfmt ", "x"} {
		if err := os.WriteFile("notes.png", []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readAssets([]string{"notes.png"}); err == nil || !strings.Contains(err.Error(), "is not a PNG, JPEG, WebP, MP4, MOV, or WebM file") {
			t.Errorf("accepted %q: %v", content, err)
		}
	}
}

func TestUnsupportedImageUploadDoesNotCreateMR(t *testing.T) {
	checkout(t)
	if err := os.WriteFile("asset.png", []byte(pngMagic+"asset"), 0600); err != nil {
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
	if err == nil || !strings.Contains(err.Error(), "does not support merge request attachments yet") || strings.Contains(err.Error(), "upgrade the server") {
		t.Fatalf("expected a readable unsupported-upload error, got %v", err)
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
	uploaded, err := o.uploadAssets(cmd, "head", []localAsset{{name: "demo.mp4", content: []byte("video")}})
	if err != nil || len(uploaded) != 1 || uploaded[0].markdown() != "\n\n![demo.mp4]("+assetURL+")" {
		t.Fatalf("uploaded=%v err=%v", uploaded, err)
	}
}

// existingMRServer serves an open MR whose description is saved by updates, and
// like the API returns the same asset for a repeated upload key. It reports the update count.
func existingMRServer(t *testing.T, head string) (*code.MergeRequest, *int) {
	t.Helper()
	mux := apiServer(t)
	existing := fixture(head)
	getRPC(t, mux, existing, "feature")
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUploadMergeRequestAssetProcedure, func(req *code.UploadMergeRequestAssetRequest) (*code.UploadMergeRequestAssetResponse, error) {
		id := "mra_" + req.UploadKey[:8]
		return &code.UploadMergeRequestAssetResponse{AssetId: id, Url: "https://depot.dev/api/code/mr-assets/test-org/repo/" + id + ".png"}, nil
	})
	updates := 0
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUpdateMergeRequestProcedure, func(req *code.UpdateMergeRequestRequest) (*code.UpdateMergeRequestResponse, error) {
		updates++
		existing.Body = req.GetBody()
		return &code.UpdateMergeRequestResponse{MergeRequest: existing}, nil
	})
	return existing, &updates
}

func TestRepeatedAttachToExistingMRAddsNothing(t *testing.T) {
	existing, updates := existingMRServer(t, checkout(t))
	if err := os.WriteFile("asset.png", []byte(pngMagic+"asset"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"create", "--title", "Images", "--attach", "asset.png"}
	if _, err := execute(t, args...); err != nil || *updates != 1 {
		t.Fatalf("first run: updates=%d err=%v", *updates, err)
	}
	attached := existing.Body
	// A renamed copy has the same bytes, so it is already attached too.
	if err := os.WriteFile("renamed.png", []byte(pngMagic+"asset"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"asset.png", "renamed.png"} {
		args[len(args)-1] = path
		out, err := execute(t, args...)
		if err != nil || *updates != 1 || existing.Body != attached || !strings.Contains(out, "mr_1: Test change") {
			t.Fatalf("%s: updates=%d body=%q out=%s err=%v", path, *updates, existing.Body, out, err)
		}
	}
}

func TestAttachToExistingMRAddsOnlyNewFiles(t *testing.T) {
	existing, updates := existingMRServer(t, checkout(t))
	for _, path := range []string{"old.png", "new.png"} {
		if err := os.WriteFile(path, []byte(pngMagic+path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := execute(t, "create", "--title", "Images", "--attach", "old.png"); err != nil {
		t.Fatal(err)
	}
	attached := existing.Body
	out, err := execute(t, "create", "--title", "Images", "--attach", "old.png", "--attach", "new.png")
	added := strings.TrimPrefix(existing.Body, attached)
	if err != nil || *updates != 2 || !strings.HasPrefix(added, "\n\n![new.png](") || strings.Count(existing.Body, "![") != 2 || !strings.Contains(out, "mr_1") {
		t.Fatalf("updates=%d body=%q out=%s err=%v", *updates, existing.Body, out, err)
	}
}

// The API refuses an asset that expired or is bound to an earlier MR; the CLI
// then uploads a new copy instead of failing on every run.
func TestCreateUploadsNewCopyWhenEarlierUploadIsUnusable(t *testing.T) {
	checkout(t)
	if err := os.WriteFile("asset.png", []byte(pngMagic+"asset"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := apiServer(t)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no MR"))
	})
	var keys []string
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUploadMergeRequestAssetProcedure, func(req *code.UploadMergeRequestAssetRequest) (*code.UploadMergeRequestAssetResponse, error) {
		keys = append(keys, req.UploadKey)
		if len(keys) == 1 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("Draft asset expired; start a new upload"))
		}
		return &code.UploadMergeRequestAssetResponse{AssetId: "mra_2", Url: "https://depot.dev/api/code/mr-assets/test-org/repo/mra_2.png"}, nil
	})
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(req *code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		mr := fixture("abc")
		mr.Body = req.Body
		return &code.CreateMergeRequestResponse{MergeRequest: mr}, nil
	})
	out, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if err != nil || len(keys) != 2 || keys[0] == keys[1] || !strings.Contains(out, "mra_2") {
		t.Fatalf("keys=%v out=%s err=%v", keys, out, err)
	}
}

func TestAttachToExistingMRStaysIdempotentAfterNewCopy(t *testing.T) {
	head := checkout(t)
	if err := os.WriteFile("asset.png", []byte(pngMagic+"asset"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := apiServer(t)
	existing := fixture(head)
	getRPC(t, mux, existing, "feature")
	var stale string
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUploadMergeRequestAssetProcedure, func(req *code.UploadMergeRequestAssetRequest) (*code.UploadMergeRequestAssetResponse, error) {
		id := "mra_" + req.UploadKey[:8]
		if stale == "" {
			stale = id
		}
		return &code.UploadMergeRequestAssetResponse{AssetId: id, Url: "https://depot.dev/api/code/mr-assets/test-org/repo/" + id + ".png"}, nil
	})
	updates := 0
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUpdateMergeRequestProcedure, func(req *code.UpdateMergeRequestRequest) (*code.UpdateMergeRequestResponse, error) {
		// The branch-keyed asset belongs to an earlier MR from this branch name.
		if strings.Contains(req.GetBody(), stale) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("Asset is unavailable; upload it again before saving"))
		}
		updates++
		existing.Body = req.GetBody()
		return &code.UpdateMergeRequestResponse{MergeRequest: existing}, nil
	})
	for run := 1; run <= 2; run++ {
		out, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
		if err != nil || updates != 1 || strings.Count(existing.Body, "![") != 1 || !strings.Contains(out, "mr_1") {
			t.Fatalf("run %d: updates=%d body=%q out=%s err=%v", run, updates, existing.Body, out, err)
		}
	}
}
