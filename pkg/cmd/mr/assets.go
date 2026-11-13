package mr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

const maxAssetBytes = 10 * 1024 * 1024

type localAsset struct {
	name    string
	content []byte
}

type attachment struct {
	alt string
	url string
}

func (a attachment) markdown() string {
	return fmt.Sprintf("\n\n![%s](%s)", a.alt, a.url)
}

// uploadKey depends only on the scope (normally the branch) and the bytes, so the
// API returns the same asset and URL when the same file is uploaded again.
func uploadKey(scope string, content []byte) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, scope+"\x00")
	_, _ = hash.Write(content)
	return hex.EncodeToString(hash.Sum(nil))
}

// saveWithAssets uploads under the stable key and passes the result to save. The API
// refuses to save an asset bound to an earlier merge request, so that rejection gets
// one more attempt with new copies; save then also receives the first uploads.
func (o *options) saveWithAssets(cmd *cobra.Command, head, salt, unchanged string, assets []localAsset, save func(uploaded, earlier []attachment) error) error {
	salted := head + "\x00" + salt
	first, err := o.uploadAssets(cmd, head, salted, assets)
	if err != nil {
		return fmt.Errorf("%w (%s)", err, unchanged)
	}
	err = save(first, nil)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		return err
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "An earlier upload of these attachments cannot be reused; uploading new copies")
	second, err := o.uploadAssets(cmd, salted, salted, assets)
	if err != nil {
		return fmt.Errorf("%w (%s)", err, unchanged)
	}
	return save(second, first)
}

// upload sends one asset, retrying transient failures with the same identity.
func (o *options) upload(cmd *cobra.Command, message *code.UploadMergeRequestAssetRequest) (*connect.Response[code.UploadMergeRequestAssetResponse], error) {
	client := api.NewMergeRequestClient()
	for attempt := 0; ; attempt++ {
		response, err := client.UploadMergeRequestAsset(cmd.Context(), request(o, message))
		if attempt == 2 || err == nil || (connect.CodeOf(err) != connect.CodeUnavailable && connect.CodeOf(err) != connect.CodeDeadlineExceeded) {
			return response, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-cmd.Context().Done():
			timer.Stop()
			return nil, cmd.Context().Err()
		case <-timer.C:
		}
	}
}

// Read every file before uploading so a missing later file cannot partially publish the request.
// The API validates the asset decoder and pixel limits from these exact bytes.
func readAssets(paths []string) ([]localAsset, error) {
	if len(paths) > 10 {
		return nil, fmt.Errorf("at most 10 --attach files are allowed")
	}
	assets := make([]localAsset, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("asset %q: %w", path, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxAssetBytes {
			return nil, fmt.Errorf("asset %q must be a regular file containing 1 byte to 10 MiB", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("asset %q: %w", path, err)
		}
		content, err := io.ReadAll(io.LimitReader(file, maxAssetBytes+1))
		closeErr := file.Close()
		if err != nil {
			return nil, fmt.Errorf("asset %q: %w", path, err)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(content) == 0 || len(content) > maxAssetBytes {
			return nil, fmt.Errorf("asset %q changed size while reading", path)
		}
		if !supportedAsset(content) {
			return nil, fmt.Errorf("asset %q is not a PNG, JPEG, WebP, MP4, MOV, or WebM file", path)
		}
		assets = append(assets, localAsset{name: filepath.Base(path), content: content})
	}
	return assets, nil
}

// supportedAsset reports whether content starts like a format the API accepts.
// It only catches obvious mistakes early; the API still decodes every upload.
func supportedAsset(content []byte) bool {
	for _, magic := range []string{"\x89PNG\r\n\x1a\n", "\xff\xd8\xff", "\x1a\x45\xdf\xa3"} {
		if bytes.HasPrefix(content, []byte(magic)) {
			return true
		}
	}
	if len(content) < 12 {
		return false
	}
	if string(content[:4]) == "RIFF" && string(content[8:12]) == "WEBP" {
		return true
	}
	// MP4 and MOV open with an atom; QuickTime may omit ftyp.
	switch string(content[4:8]) {
	case "ftyp", "wide", "mdat", "moov", "free", "skip":
		return true
	}
	return false
}

// uploadAssets uploads each asset under scope. An upload that expired before it
// was saved is refused, so that asset is uploaded again under fallback.
func (o *options) uploadAssets(cmd *cobra.Command, scope, fallback string, assets []localAsset) ([]attachment, error) {
	result := make([]attachment, 0, len(assets))
	for index, asset := range assets {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Uploading asset %d/%d: %s\n", index+1, len(assets), asset.name)
		message := &code.UploadMergeRequestAssetRequest{
			RepositoryId: o.repo, UploadKey: uploadKey(scope, asset.content), Content: asset.content,
		}
		response, err := o.upload(cmd, message)
		if connect.CodeOf(err) == connect.CodeFailedPrecondition && fallback != scope {
			message.UploadKey = uploadKey(fallback, asset.content)
			response, err = o.upload(cmd, message)
		}
		if connect.CodeOf(err) == connect.CodeUnimplemented {
			return nil, fmt.Errorf("upload asset %q: this Depot API does not support merge request attachments yet; retry without --attach", asset.name)
		}
		if err != nil {
			return nil, fmt.Errorf("upload asset %q: %w", asset.name, err)
		}
		parsed, err := url.Parse(response.Msg.Url)
		if err != nil {
			return nil, fmt.Errorf("upload asset %q returned an invalid asset URL", asset.name)
		}
		extension := path.Ext(parsed.Path)
		validExtension := extension == ".png" || extension == ".jpg" || extension == ".webp" || extension == ".mp4" || extension == ".mov" || extension == ".webm"
		prefix := "/api/code/mr-assets/"
		parts := strings.Split(strings.TrimPrefix(parsed.Path, prefix), "/")
		validPath := strings.HasPrefix(parsed.Path, prefix) && len(parts) == 3 && parts[0] != "" && (o.org == "" || parts[0] == o.org) && parts[1] == o.repo && parts[2] == response.Msg.AssetId+extension
		validScheme := parsed.Scheme == "https" || (parsed.Scheme == "http" && strings.HasPrefix(os.Getenv("DEPOT_API_URL"), "http://"))
		if !validExtension || !validScheme || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || response.Msg.AssetId == "" || !validPath {
			return nil, fmt.Errorf("upload asset %q returned an invalid asset URL", asset.name)
		}
		alt := strings.NewReplacer("\\", " ", "[", " ", "]", " ", "\r", " ", "\n", " ").Replace(asset.name)
		result = append(result, attachment{alt: alt, url: response.Msg.Url})
	}
	return result, nil
}
