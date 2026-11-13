package mr

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/depot/cli/pkg/proto/depot/code/v1beta2/codev1beta2connect"
	"github.com/spf13/cobra"
)

// Asset fixtures only need a supported signature; nothing decodes them.
const (
	pngMagic = "\x89PNG\r\n\x1a\n"
	mp4Magic = "\x00\x00\x00\x18ftypmp42"
)

type fakeAsset struct {
	id, key, contentType string
	size                 int64
	stored               []byte
	ready                bool
}

// assetAPI fakes the upload RPCs and the storage they sign URLs for, following the
// API contract (DEP-8380): one asset per upload key, a conditional PUT, then complete.
type assetAPI struct {
	t       *testing.T
	storage *httptest.Server
	origin  string
	org     string
	assets  []*fakeAsset
	begins  []string
	puts    int
	// Hooks run before the fake's own behaviour; an error or a status replaces it.
	onBegin    func(call int, req *code.BeginMergeRequestAssetUploadRequest) error
	onPut      func(call int) int
	onComplete func(call int) error
	completes  int
}

var extensions = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "video/mp4": ".mp4", "video/quicktime": ".mov", "video/webm": ".webm"}

func newAssetAPI(t *testing.T, mux *http.ServeMux) *assetAPI {
	t.Helper()
	f := &assetAPI{t: t, origin: "https://depot.dev", org: "test-org"}
	f.storage = httptest.NewServer(http.HandlerFunc(f.put))
	t.Cleanup(f.storage.Close)
	handle(f, mux, codev1beta2connect.MergeRequestServiceBeginMergeRequestAssetUploadProcedure, f.begin)
	handle(f, mux, codev1beta2connect.MergeRequestServiceCompleteMergeRequestAssetUploadProcedure, f.complete)
	return f
}

func handle[Req, Resp any](f *assetAPI, mux *http.ServeMux, path string, call func(*Req) (*Resp, error)) {
	mux.Handle(path, connect.NewUnaryHandler(path, func(_ context.Context, req *connect.Request[Req]) (*connect.Response[Resp], error) {
		if req.Header().Get("Authorization") != "Bearer local-test-token" || req.Header().Get("X-Depot-Org") != f.org {
			f.t.Error("unexpected authentication or organization header")
		}
		response, err := call(req.Msg)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(response), nil
	}))
}

func (f *assetAPI) url(asset *fakeAsset) string {
	org := f.org
	if org == "" {
		org = "resolved-org"
	}
	return f.origin + "/api/code/mr-assets/" + org + "/repo/" + asset.id + extensions[asset.contentType]
}

func (f *assetAPI) begin(req *code.BeginMergeRequestAssetUploadRequest) (*code.BeginMergeRequestAssetUploadResponse, error) {
	f.begins = append(f.begins, req.UploadKey)
	if f.onBegin != nil {
		if err := f.onBegin(len(f.begins), req); err != nil {
			return nil, err
		}
	}
	if req.RepositoryId != "repo" || len(req.UploadKey) != 64 || req.Size < 1 || extensions[req.ContentType] == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid upload"))
	}
	var asset *fakeAsset
	for _, known := range f.assets {
		if known.key == req.UploadKey {
			asset = known
		}
	}
	if asset == nil {
		asset = &fakeAsset{id: fmt.Sprintf("mra_%d", len(f.assets)+1), key: req.UploadKey, contentType: req.ContentType, size: req.Size}
		f.assets = append(f.assets, asset)
	}
	if asset.size != req.Size || asset.contentType != req.ContentType {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("upload key reused for a different file"))
	}
	response := &code.BeginMergeRequestAssetUploadResponse{AssetId: asset.id, Url: f.url(asset), Uploaded: asset.ready}
	if !asset.ready {
		response.UploadUrl = fmt.Sprintf("%s/%s?signature=%d", f.storage.URL, asset.id, len(f.begins))
		response.UploadHeaders = map[string]string{"content-type": asset.contentType, "if-none-match": "*"}
	}
	return response, nil
}

func (f *assetAPI) put(w http.ResponseWriter, r *http.Request) {
	f.puts++
	if r.Header.Get("Authorization") != "" || r.Header.Get("X-Depot-Org") != "" {
		f.t.Error("Depot credentials were sent to storage")
	}
	if f.onPut != nil {
		if status := f.onPut(f.puts); status != 0 {
			w.WriteHeader(status)
			return
		}
	}
	var asset *fakeAsset
	for _, known := range f.assets {
		if "/"+known.id == r.URL.Path {
			asset = known
		}
	}
	if asset == nil || r.Method != http.MethodPut || r.ContentLength != asset.size || r.Header.Get("Content-Type") != asset.contentType || r.Header.Get("If-None-Match") != "*" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if asset.stored != nil {
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	asset.stored, _ = io.ReadAll(r.Body)
}

func (f *assetAPI) complete(req *code.CompleteMergeRequestAssetUploadRequest) (*code.CompleteMergeRequestAssetUploadResponse, error) {
	f.completes++
	if f.onComplete != nil {
		if err := f.onComplete(f.completes); err != nil {
			return nil, err
		}
	}
	for _, asset := range f.assets {
		if asset.id != req.AssetId {
			continue
		}
		if asset.stored == nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("not uploaded yet"))
		}
		asset.ready = true
		return &code.CompleteMergeRequestAssetUploadResponse{AssetId: asset.id, Url: f.url(asset)}, nil
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no asset"))
}

func writeAsset(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func noMR(t *testing.T, mux *http.ServeMux) {
	t.Helper()
	rpc(t, mux, codev1beta2connect.MergeRequestServiceGetMergeRequestProcedure, func(*code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no MR"))
	})
}

// createRPC opens an MR with the requested description and counts the calls.
func createRPC(t *testing.T, mux *http.ServeMux) *int {
	t.Helper()
	created := 0
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(req *code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		created++
		mr := fixture("abc")
		mr.Body = req.Body
		return &code.CreateMergeRequestResponse{MergeRequest: mr}, nil
	})
	return &created
}

func TestCreateUploadsOrderedImagesAndVideosWithBothFlagNames(t *testing.T) {
	checkout(t)
	writeAsset(t, "before.png", pngMagic+"before")
	writeAsset(t, "demo.mp4", mp4Magic+"demo")
	mux := apiServer(t)
	noMR(t, mux)
	fake := newAssetAPI(t, mux)
	rpc(t, mux, codev1beta2connect.MergeRequestServiceCreateMergeRequestProcedure, func(req *code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
		want := "Description\n\n![before.png](https://depot.dev/api/code/mr-assets/test-org/repo/mra_1.png)\n\n![demo.mp4](https://depot.dev/api/code/mr-assets/test-org/repo/mra_2.mp4)"
		if req.Body != want {
			t.Errorf("unexpected body: %s", req.Body)
		}
		mr := fixture("abc")
		mr.Body = req.Body
		return &code.CreateMergeRequestResponse{MergeRequest: mr}, nil
	})
	out, err := execute(t, "create", "--title", "Screenshots", "--body", "Description", "--image", "before.png", "--attach", "demo.mp4", "--json")
	if err != nil || !strings.Contains(out, "mra_2") {
		t.Fatalf("%s: %v", out, err)
	}
	if len(fake.assets) != 2 || string(fake.assets[0].stored) != pngMagic+"before" || string(fake.assets[1].stored) != mp4Magic+"demo" || !fake.assets[1].ready {
		t.Fatalf("unexpected stored assets: %+v", fake.assets)
	}
}

func TestFailedImageNeverCreatesMRAndRetryKeepsUploadIdentity(t *testing.T) {
	checkout(t)
	writeAsset(t, "asset.png", pngMagic+"asset")
	mux := apiServer(t)
	noMR(t, mux)
	fake := newAssetAPI(t, mux)
	fake.onBegin = func(call int, _ *code.BeginMergeRequestAssetUploadRequest) error {
		if call == 1 {
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("lost response"))
		}
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid asset"))
	}
	created := createRPC(t, mux)
	_, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if err == nil || len(fake.begins) != 2 || fake.begins[0] != fake.begins[1] || *created != 0 || !strings.Contains(err.Error(), "invalid asset") {
		t.Fatalf("begins=%v created=%d err=%v", fake.begins, *created, err)
	}
}

func TestReadAssetsBoundsFilesAndRejectsMissingLaterFile(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, size := range []int64{0, maxAssetBytes + 1} {
		writeAsset(t, "asset.png", "")
		if err := os.Truncate("asset.png", size); err != nil {
			t.Fatal(err)
		}
		if _, err := readAssets([]string{"asset.png"}); err == nil || !strings.Contains(err.Error(), "1 byte to 100 MiB") {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	writeAsset(t, "asset.png", pngMagic+"valid")
	if _, err := readAssets([]string{"asset.png", "missing.png"}); err == nil {
		t.Fatal("accepted missing asset")
	}
	if _, err := readAssets([]string{"."}); err == nil {
		t.Fatal("accepted directory")
	}
}

func TestReadAssetsHashesLargestAllowedFileFromDisk(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAsset(t, "big.png", pngMagic)
	if err := os.Truncate("big.png", maxAssetBytes); err != nil {
		t.Fatal(err)
	}
	assets, err := readAssets([]string{"big.png"})
	if err != nil || assets[0].size != maxAssetBytes || assets[0].contentType != "image/png" || len(assets[0].digest) != 32 {
		t.Fatalf("assets=%+v err=%v", assets, err)
	}
}

func TestCreateAttachesToExistingMRDescription(t *testing.T) {
	head := checkout(t)
	writeAsset(t, "asset.png", pngMagic+"asset")
	mux := apiServer(t)
	existing := fixture(head)
	getRPC(t, mux, existing, "feature")
	assetURL := "https://depot.dev/api/code/mr-assets/test-org/repo/mra_1.png"
	fake := newAssetAPI(t, mux)
	fake.onBegin = func(int, *code.BeginMergeRequestAssetUploadRequest) error {
		// Someone edits the description while the upload is in flight.
		existing.Body = "Edited meanwhile"
		return nil
	}
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
	writeAsset(t, "asset.png", pngMagic+"asset")
	mux := apiServer(t)
	noMR(t, mux)
	newAssetAPI(t, mux)
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
	writeAsset(t, "asset.png", pngMagic+"asset")
	mux := apiServer(t)
	getRPC(t, mux, fixture(head), "feature")
	fake := newAssetAPI(t, mux)
	// The API deletes a stored file whose size or leading bytes do not match.
	fake.onComplete = func(int) error { return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid asset")) }
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUpdateMergeRequestProcedure, func(*code.UpdateMergeRequestRequest) (*code.UpdateMergeRequestResponse, error) {
		t.Error("updated MR after a failed upload")
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unexpected update"))
	})
	_, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if err == nil || !strings.Contains(err.Error(), "invalid asset") || !strings.Contains(err.Error(), "mr_1 was not changed") {
		t.Fatalf("err=%v", err)
	}
}

func TestReadAssetsNamesTheTypeAndRejectsUnsupportedContent(t *testing.T) {
	t.Chdir(t.TempDir())
	accepted := map[string]string{
		pngMagic:                               "image/png",
		"\xff\xd8\xff\xe0":                     "image/jpeg",
		"RIFF\x00\x00\x00\x00WEBPVP8 ":         "image/webp",
		mp4Magic:                               "video/mp4",
		"\x00\x00\x00\x14ftypqt  ":             "video/quicktime",
		"\x00\x00\x00\x08wide\x00\x00..":       "video/quicktime",
		"\x1a\x45\xdf\xa3\x9f\x42\x82\x84webm": "video/webm",
	}
	for content, contentType := range accepted {
		writeAsset(t, "asset", content+"rest of file")
		assets, err := readAssets([]string{"asset"})
		if err != nil || assets[0].contentType != contentType {
			t.Errorf("%q: assets=%+v err=%v", content, assets, err)
		}
	}
	rejected := []string{"plain text notes", "GIF89a", "%PDF-1.7", "RIFF\x00\x00\x00\x00WAVEfmt ", "x", "\x00\x00\x00\x18ftypheic", "\x1a\x45\xdf\xa3\x9f\x42\x82\x88matroska"}
	for _, content := range rejected {
		writeAsset(t, "notes.png", content)
		if _, err := readAssets([]string{"notes.png"}); err == nil || !strings.Contains(err.Error(), "is not a PNG, JPEG, WebP, MP4, MOV, or WebM file") {
			t.Errorf("accepted %q: %v", content, err)
		}
	}
}

func TestUnsupportedUploadDoesNotCreateMR(t *testing.T) {
	checkout(t)
	writeAsset(t, "asset.png", pngMagic+"asset")
	mux := apiServer(t)
	noMR(t, mux)
	fake := newAssetAPI(t, mux)
	fake.onBegin = func(int, *code.BeginMergeRequestAssetUploadRequest) error {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("upgrade the server"))
	}
	created := createRPC(t, mux)
	_, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if err == nil || *created != 0 || !strings.Contains(err.Error(), "does not support merge request attachments yet") || strings.Contains(err.Error(), "upgrade the server") {
		t.Fatalf("expected a readable unsupported-upload error, got %v", err)
	}
}

func TestUploadAcceptsServerResolvedOrganizationAndAppOrigin(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAsset(t, "demo.mp4", mp4Magic+"video")
	fake := newAssetAPI(t, apiServer(t))
	fake.origin, fake.org = "https://preview.depot.dev", ""
	assets, err := readAssets([]string{"demo.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	o := &options{repo: "repo", token: "local-test-token"}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetErr(io.Discard)
	uploaded, err := o.uploadAssets(cmd, assets, "head")
	want := "\n\n![demo.mp4](https://preview.depot.dev/api/code/mr-assets/resolved-org/repo/mra_1.mp4)"
	if err != nil || len(uploaded) != 1 || uploaded[0].markdown() != want {
		t.Fatalf("uploaded=%v err=%v", uploaded, err)
	}
}

// existingMRServer serves an open MR whose description is saved by updates. It
// returns the MR, the fake asset API and the update count.
func existingMRServer(t *testing.T, head string) (*code.MergeRequest, *assetAPI, *int) {
	t.Helper()
	mux := apiServer(t)
	existing := fixture(head)
	getRPC(t, mux, existing, "feature")
	fake := newAssetAPI(t, mux)
	updates := 0
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUpdateMergeRequestProcedure, func(req *code.UpdateMergeRequestRequest) (*code.UpdateMergeRequestResponse, error) {
		updates++
		existing.Body = req.GetBody()
		return &code.UpdateMergeRequestResponse{MergeRequest: existing}, nil
	})
	return existing, fake, &updates
}

func TestRepeatedAttachToExistingMRAddsNothingAndSendsNothing(t *testing.T) {
	existing, fake, updates := existingMRServer(t, checkout(t))
	writeAsset(t, "asset.png", pngMagic+"asset")
	args := []string{"create", "--title", "Images", "--attach", "asset.png"}
	if _, err := execute(t, args...); err != nil || *updates != 1 {
		t.Fatalf("first run: updates=%d err=%v", *updates, err)
	}
	attached := existing.Body
	// A renamed copy has the same bytes, so it is already attached too.
	writeAsset(t, "renamed.png", pngMagic+"asset")
	for _, path := range []string{"asset.png", "renamed.png"} {
		args[len(args)-1] = path
		out, err := execute(t, args...)
		if err != nil || *updates != 1 || fake.puts != 1 || fake.completes != 1 || existing.Body != attached || !strings.Contains(out, "mr_1: Test change") {
			t.Fatalf("%s: updates=%d puts=%d body=%q out=%s err=%v", path, *updates, fake.puts, existing.Body, out, err)
		}
	}
}

func TestAttachToExistingMRAddsOnlyNewFiles(t *testing.T) {
	existing, _, updates := existingMRServer(t, checkout(t))
	writeAsset(t, "old.png", pngMagic+"old")
	writeAsset(t, "new.png", pngMagic+"new")
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

// The API refuses a key whose draft expired or whose file it rejected; the CLI
// then uploads a new copy instead of failing on every run.
func TestCreateUploadsNewCopyWhenEarlierUploadIsUnusable(t *testing.T) {
	checkout(t)
	writeAsset(t, "asset.png", pngMagic+"asset")
	mux := apiServer(t)
	noMR(t, mux)
	fake := newAssetAPI(t, mux)
	fake.onBegin = func(call int, _ *code.BeginMergeRequestAssetUploadRequest) error {
		if call == 1 {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("Draft asset expired; start a new upload"))
		}
		return nil
	}
	createRPC(t, mux)
	out, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if err != nil || len(fake.begins) != 2 || fake.begins[0] == fake.begins[1] || fake.puts != 1 || !strings.Contains(out, "mra_1") {
		t.Fatalf("begins=%v out=%s err=%v", fake.begins, out, err)
	}
}

func TestAttachToExistingMRStaysIdempotentAfterNewCopy(t *testing.T) {
	head := checkout(t)
	writeAsset(t, "old.png", pngMagic+"old")
	writeAsset(t, "new.png", pngMagic+"new")
	mux := apiServer(t)
	existing := fixture(head)
	getRPC(t, mux, existing, "feature")
	fake := newAssetAPI(t, mux)
	updates := 0
	rpc(t, mux, codev1beta2connect.MergeRequestServiceUpdateMergeRequestProcedure, func(req *code.UpdateMergeRequestRequest) (*code.UpdateMergeRequestResponse, error) {
		// The branch-keyed asset for new.png belongs to an earlier MR from this branch name.
		for _, asset := range fake.assets {
			if string(asset.stored) != pngMagic+"new" {
				continue
			}
			if strings.Contains(req.GetBody(), asset.id+".png") {
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("Asset is unavailable; upload it again before saving"))
			}
			break
		}
		updates++
		existing.Body = req.GetBody()
		return &code.UpdateMergeRequestResponse{MergeRequest: existing}, nil
	})
	if _, err := execute(t, "create", "--title", "Images", "--attach", "old.png"); err != nil || updates != 1 {
		t.Fatalf("updates=%d err=%v", updates, err)
	}
	// old.png is attached already; only new.png gets a new copy, and only once.
	for run := 1; run <= 2; run++ {
		out, err := execute(t, "create", "--title", "Images", "--attach", "old.png", "--attach", "new.png")
		if err != nil || updates != 2 || strings.Count(existing.Body, "![old.png]") != 1 || strings.Count(existing.Body, "![new.png]") != 1 || !strings.Contains(out, "mr_1") {
			t.Fatalf("run %d: updates=%d body=%q out=%s err=%v", run, updates, existing.Body, out, err)
		}
	}
}

func TestAttachToExistingMRSurvivesExpiredNewCopy(t *testing.T) {
	existing, fake, _ := existingMRServer(t, checkout(t))
	writeAsset(t, "asset.png", pngMagic+"asset")
	keys := map[string]bool{}
	fake.onBegin = func(_ int, req *code.BeginMergeRequestAssetUploadRequest) error {
		if _, seen := keys[req.UploadKey]; !seen {
			// The branch key and the MR's first key name uploads that expired unsaved.
			keys[req.UploadKey] = len(keys) < 2
		}
		if keys[req.UploadKey] {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("Draft asset expired; start a new upload"))
		}
		return nil
	}
	for run := 0; run < 2; run++ {
		if _, err := execute(t, "create", "--title", "Images", "--attach", "asset.png"); err != nil {
			t.Fatal(err)
		}
	}
	if len(keys) != 3 || fake.puts != 1 || strings.Count(existing.Body, "mra_1") != 1 {
		t.Fatalf("keys=%d puts=%d body=%q", len(keys), fake.puts, existing.Body)
	}
}

func TestExpiredUploadURLBeginsAgainUnderTheSameKey(t *testing.T) {
	checkout(t)
	writeAsset(t, "asset.png", pngMagic+"asset")
	mux := apiServer(t)
	noMR(t, mux)
	fake := newAssetAPI(t, mux)
	fake.onPut = func(call int) int {
		if call == 1 {
			return http.StatusForbidden
		}
		return 0
	}
	created := createRPC(t, mux)
	_, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if err != nil || *created != 1 || fake.puts != 2 || len(fake.begins) != 2 || fake.begins[0] != fake.begins[1] || !bytes.Equal(fake.assets[0].stored, []byte(pngMagic+"asset")) {
		t.Fatalf("created=%d puts=%d begins=%v err=%v", *created, fake.puts, fake.begins, err)
	}
}

// A run that stored the file but never completed it is finished by the next run,
// which is told the file is already stored and does not send it again.
func TestRerunCompletesAStoredUpload(t *testing.T) {
	checkout(t)
	writeAsset(t, "asset.png", pngMagic+"asset")
	mux := apiServer(t)
	noMR(t, mux)
	fake := newAssetAPI(t, mux)
	fake.onComplete = func(call int) error {
		switch call {
		case 1:
			return connect.NewError(connect.CodeInternal, fmt.Errorf("complete failed"))
		case 2:
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("lost response"))
		}
		return nil
	}
	created := createRPC(t, mux)
	if _, err := execute(t, "create", "--title", "Images", "--attach", "asset.png"); err == nil || *created != 0 || !strings.Contains(err.Error(), "merge request was not created") {
		t.Fatalf("created=%d err=%v", *created, err)
	}
	stored := fake.assets[0].stored
	out, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if err != nil || *created != 1 || fake.puts != 2 || fake.completes != 3 || &fake.assets[0].stored[0] != &stored[0] || !strings.Contains(out, "mra_1.png") {
		t.Fatalf("created=%d puts=%d completes=%d out=%s err=%v", *created, fake.puts, fake.completes, out, err)
	}
}

func TestFileChangedAfterInspectionIsNotCompleted(t *testing.T) {
	checkout(t)
	writeAsset(t, "asset.png", pngMagic+"asset")
	mux := apiServer(t)
	noMR(t, mux)
	fake := newAssetAPI(t, mux)
	fake.onBegin = func(int, *code.BeginMergeRequestAssetUploadRequest) error {
		writeAsset(t, "asset.png", pngMagic+"other")
		return nil
	}
	created := createRPC(t, mux)
	_, err := execute(t, "create", "--title", "Images", "--attach", "asset.png")
	if err == nil || *created != 0 || fake.completes != 0 || !strings.Contains(err.Error(), "changed while it was uploading") {
		t.Fatalf("created=%d completes=%d err=%v", *created, fake.completes, err)
	}
}
