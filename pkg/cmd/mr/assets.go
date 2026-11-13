package mr

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

const maxAssetBytes = 100 * 1024 * 1024

// localAsset describes a file to upload. The bytes stay on disk and are streamed.
type localAsset struct {
	name        string
	path        string
	contentType string
	size        int64
	digest      []byte
}

type attachment struct {
	alt string
	url string
}

func (a attachment) markdown() string {
	return fmt.Sprintf("\n\n![%s](%s)", a.alt, a.url)
}

// uploadKey depends only on the scope (normally the branch) and the file's SHA-256,
// so the API returns the same asset and URL when the same file is uploaded again.
func uploadKey(scope string, digest []byte) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, scope+"\x00")
	_, _ = hash.Write(digest)
	return hex.EncodeToString(hash.Sum(nil))
}

// copyScopes names the new copies. A merge request's copies get a fixed series so
// a re-run finds the one it attached; with no merge request yet the name is random.
func copyScopes(head, salt string) ([]string, error) {
	if salt != "" {
		salted := head + "\x00" + salt
		return []string{salted, salted + "\x001", salted + "\x002"}, nil
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	return []string{head + "\x00" + hex.EncodeToString(random[:])}, nil
}

// saveWithAssets uploads under the stable key and passes the result to save. The API
// refuses to save an asset bound to an earlier merge request, so that rejection gets
// one more attempt with new copies; save then also receives the first uploads.
func (o *options) saveWithAssets(cmd *cobra.Command, head, salt, unchanged string, assets []localAsset, save func(uploaded, earlier []attachment) error) error {
	copies, err := copyScopes(head, salt)
	if err != nil {
		return err
	}
	first, err := o.uploadAssets(cmd, assets, append([]string{head}, copies...)...)
	if err != nil {
		return fmt.Errorf("%w (%s)", err, unchanged)
	}
	err = save(first, nil)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		return err
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "An earlier upload of these attachments cannot be reused; uploading new copies")
	second, err := o.uploadAssets(cmd, assets, copies...)
	if err != nil {
		return fmt.Errorf("%w (%s)", err, unchanged)
	}
	return save(second, first)
}

func pause(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retry repeats an idempotent call that failed in transit, up to three attempts.
func retry[T any](ctx context.Context, call func() (*connect.Response[T], error)) (*connect.Response[T], error) {
	for attempt := 0; ; attempt++ {
		response, err := call()
		if attempt == 2 || err == nil || (connect.CodeOf(err) != connect.CodeUnavailable && connect.CodeOf(err) != connect.CodeDeadlineExceeded) {
			return response, err
		}
		if err := pause(ctx, attempt); err != nil {
			return nil, err
		}
	}
}

// Inspect every file before uploading so a missing later file cannot partially publish the request.
func readAssets(paths []string) ([]localAsset, error) {
	if len(paths) > 10 {
		return nil, fmt.Errorf("at most 10 --attach files are allowed")
	}
	assets := make([]localAsset, 0, len(paths))
	for _, path := range paths {
		asset, err := inspectAsset(path)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

// inspectAsset checks the size and format of a file and hashes it in one streamed pass.
func inspectAsset(path string) (localAsset, error) {
	info, err := os.Stat(path)
	if err != nil {
		return localAsset{}, fmt.Errorf("asset %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxAssetBytes {
		return localAsset{}, fmt.Errorf("asset %q must be a regular file containing 1 byte to 100 MiB", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return localAsset{}, fmt.Errorf("asset %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	head := make([]byte, 1024)
	read, err := io.ReadFull(io.TeeReader(file, hash), head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return localAsset{}, fmt.Errorf("asset %q: %w", path, err)
	}
	contentType := assetType(head[:read])
	if contentType == "" {
		return localAsset{}, fmt.Errorf("asset %q is not a PNG, JPEG, WebP, MP4, MOV, or WebM file", path)
	}
	rest, err := io.Copy(hash, file)
	if err != nil {
		return localAsset{}, fmt.Errorf("asset %q: %w", path, err)
	}
	if int64(read)+rest != info.Size() {
		return localAsset{}, fmt.Errorf("asset %q changed size while reading", path)
	}
	return localAsset{name: filepath.Base(path), path: path, contentType: contentType, size: info.Size(), digest: hash.Sum(nil)}, nil
}

// assetType names the MIME type the leading bytes of a file identify, or "" for an
// unsupported file. The API repeats this check on the stored file.
func assetType(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(head, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(head, []byte("\x1a\x45\xdf\xa3")):
		// Matroska shares this signature; only the WebM document type is accepted.
		if bytes.Contains(head, []byte("\x42\x82\x84webm")) {
			return "video/webm"
		}
		return ""
	case len(head) < 12:
		return ""
	case string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "image/webp"
	}
	// MP4 and MOV open with an atom; QuickTime may omit ftyp.
	switch string(head[4:8]) {
	case "ftyp":
		brand := string(head[8:12])
		if slices.Contains([]string{"heic", "heix", "mif1", "msf1", "avif", "avis", "M4A ", "M4B ", "M4P "}, brand) {
			return ""
		}
		if brand == "qt  " {
			return "video/quicktime"
		}
		return "video/mp4"
	case "wide", "mdat", "moov", "free", "skip":
		return "video/quicktime"
	}
	return ""
}

// uploadAssets uploads each asset and returns its reference for the description.
func (o *options) uploadAssets(cmd *cobra.Command, assets []localAsset, scopes ...string) ([]attachment, error) {
	result := make([]attachment, 0, len(assets))
	for index, asset := range assets {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Uploading asset %d/%d: %s\n", index+1, len(assets), asset.name)
		id, assetURL, err := o.uploadAsset(cmd, asset, scopes)
		if connect.CodeOf(err) == connect.CodeUnimplemented {
			return nil, fmt.Errorf("upload asset %q: this Depot API does not support merge request attachments yet; retry without --attach", asset.name)
		}
		if err != nil {
			return nil, fmt.Errorf("upload asset %q: %w", asset.name, err)
		}
		if !o.validAssetURL(id, assetURL) {
			return nil, fmt.Errorf("upload asset %q returned an invalid asset URL", asset.name)
		}
		alt := strings.NewReplacer("\\", " ", "[", " ", "]", " ", "\r", " ", "\n", " ").Replace(asset.name)
		result = append(result, attachment{alt: alt, url: assetURL})
	}
	return result, nil
}

// uploadAsset begins the upload under the first scope. A key whose draft expired or
// whose file was rejected is refused, so the asset is then begun under the next scope.
func (o *options) uploadAsset(cmd *cobra.Command, asset localAsset, scopes []string) (id, assetURL string, err error) {
	var key string
	var started *code.BeginMergeRequestAssetUploadResponse
	for _, scope := range scopes {
		key = uploadKey(scope, asset.digest)
		started, err = o.begin(cmd, asset, key)
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			break
		}
	}
	if err != nil {
		return "", "", err
	}
	if started.Uploaded {
		return started.AssetId, started.Url, nil
	}
	if err := o.store(cmd, asset, key, started); err != nil {
		return "", "", err
	}
	client := api.NewMergeRequestClient()
	completed, err := retry(cmd.Context(), func() (*connect.Response[code.CompleteMergeRequestAssetUploadResponse], error) {
		return client.CompleteMergeRequestAssetUpload(cmd.Context(), request(o, &code.CompleteMergeRequestAssetUploadRequest{
			RepositoryId: o.repo, AssetId: started.AssetId,
		}))
	})
	if err != nil {
		return "", "", err
	}
	return completed.Msg.AssetId, completed.Msg.Url, nil
}

func (o *options) begin(cmd *cobra.Command, asset localAsset, key string) (*code.BeginMergeRequestAssetUploadResponse, error) {
	client := api.NewMergeRequestClient()
	response, err := retry(cmd.Context(), func() (*connect.Response[code.BeginMergeRequestAssetUploadResponse], error) {
		return client.BeginMergeRequestAssetUpload(cmd.Context(), request(o, &code.BeginMergeRequestAssetUploadRequest{
			RepositoryId: o.repo, UploadKey: key, Size: asset.size, ContentType: asset.contentType,
		}))
	})
	if err != nil {
		return nil, err
	}
	target, err := url.Parse(response.Msg.UploadUrl)
	if !response.Msg.Uploaded && (err != nil || target.Host == "" || !allowedScheme(target)) {
		return nil, fmt.Errorf("the API returned an invalid upload URL")
	}
	return response.Msg, nil
}

// store sends the file to storage, up to three times. A refused URL has expired,
// so the upload is begun again under the same key for a new one.
func (o *options) store(cmd *cobra.Command, asset localAsset, key string, started *code.BeginMergeRequestAssetUploadResponse) error {
	ctx := cmd.Context()
	for attempt := 0; ; attempt++ {
		status, err := put(ctx, asset, started)
		// 412 means storage already holds the file from an earlier attempt.
		if err == nil && (status/100 == 2 || status == http.StatusPreconditionFailed) {
			return nil
		}
		if err == nil {
			err = fmt.Errorf("storage returned HTTP %d", status)
		}
		transient := status == 0 || status == http.StatusForbidden || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
		if attempt == 2 || !transient || errors.Is(err, errAssetChanged) || ctx.Err() != nil {
			return err
		}
		if status != http.StatusForbidden {
			if err := pause(ctx, attempt); err != nil {
				return err
			}
			continue
		}
		if started, err = o.begin(cmd, asset, key); err != nil {
			return err
		}
		if started.Uploaded {
			return nil
		}
	}
}

var errAssetChanged = errors.New("the file changed while it was uploading; run the command again")

// checkedBody reads the file for a PUT and holds back its final chunk until the
// bytes match the inspected digest, so storage never keeps a file that changed.
type checkedBody struct {
	file      io.Reader
	hash      hash.Hash
	remaining int64
	digest    []byte
}

func (b *checkedBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	n, err := b.file.Read(p[:min(int64(len(p)), b.remaining)])
	b.hash.Write(p[:n])
	b.remaining -= int64(n)
	if err != nil && b.remaining > 0 {
		return 0, errAssetChanged
	}
	if b.remaining == 0 && !bytes.Equal(b.hash.Sum(nil), b.digest) {
		return 0, errAssetChanged
	}
	return n, nil
}

// put streams the file to the signed URL and returns the HTTP status. It uses a
// plain client so that no Depot credential reaches storage.
func put(ctx context.Context, asset localAsset, started *code.BeginMergeRequestAssetUploadResponse) (int, error) {
	file, err := os.Open(asset.path)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", errAssetChanged, err)
	}
	defer func() { _ = file.Close() }()
	body := &checkedBody{file: file, hash: sha256.New(), remaining: asset.size, digest: asset.digest}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, started.UploadUrl, body)
	if err != nil {
		return 0, err
	}
	req.ContentLength = asset.size
	// Storage answers a refused request before the body is sent.
	req.Header.Set("Expect", "100-continue")
	for name, value := range started.UploadHeaders {
		req.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(req)
	var failed *url.Error
	if errors.As(err, &failed) {
		// The wrapped error would print the signed URL.
		return 0, failed.Err
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, nil
}

func allowedScheme(parsed *url.URL) bool {
	return parsed.Scheme == "https" || (parsed.Scheme == "http" && strings.HasPrefix(os.Getenv("DEPOT_API_URL"), "http://"))
}

// validAssetURL reports whether assetURL is this repository's reference for asset id.
func (o *options) validAssetURL(id, assetURL string) bool {
	parsed, err := url.Parse(assetURL)
	if err != nil || id == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !allowedScheme(parsed) {
		return false
	}
	extension := path.Ext(parsed.Path)
	if !slices.Contains([]string{".png", ".jpg", ".webp", ".mp4", ".mov", ".webm"}, extension) {
		return false
	}
	prefix := "/api/code/mr-assets/"
	parts := strings.Split(strings.TrimPrefix(parsed.Path, prefix), "/")
	return strings.HasPrefix(parsed.Path, prefix) && len(parts) == 3 && parts[0] != "" && (o.org == "" || parts[0] == o.org) && parts[1] == o.repo && parts[2] == id+extension
}
