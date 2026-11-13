package mr

import (
	"crypto/rand"
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

// Read every file before uploading so a missing later file cannot partially publish the request.
// The API validates the asset decoder, format, and pixel limits from these exact bytes.
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
		assets = append(assets, localAsset{name: filepath.Base(path), content: content})
	}
	return assets, nil
}

func (o *options) uploadAssets(cmd *cobra.Command, head, title, body string, assets []localAsset) (string, error) {
	result := body
	client := api.NewMergeRequestClient()
	for index, asset := range assets {
		// Each explicit upload is new; transport retries below retain this identity.
		// Looking up an existing MR before uploading also handles a lost create response.
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", err
		}
		hash := sha256.New()
		_, _ = hash.Write(nonce[:])
		for _, value := range []string{head, title, body, fmt.Sprint(index), asset.name} {
			_, _ = io.WriteString(hash, value+"\x00")
		}
		_, _ = hash.Write(asset.content)
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Uploading asset %d/%d: %s\n", index+1, len(assets), asset.name)
		message := &code.UploadMergeRequestAssetRequest{
			RepositoryId: o.repo, UploadKey: hex.EncodeToString(hash.Sum(nil)), Content: asset.content,
		}
		var response *connect.Response[code.UploadMergeRequestAssetResponse]
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			response, err = client.UploadMergeRequestAsset(cmd.Context(), request(o, message))
			if err == nil || (connect.CodeOf(err) != connect.CodeUnavailable && connect.CodeOf(err) != connect.CodeDeadlineExceeded) {
				break
			}
			if attempt < 2 {
				timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
				select {
				case <-cmd.Context().Done():
					timer.Stop()
					return "", cmd.Context().Err()
				case <-timer.C:
				}
			}
		}
		if err != nil {
			return "", fmt.Errorf("upload asset %q: %w (merge request was not created)", asset.name, err)
		}
		parsed, err := url.Parse(response.Msg.Url)
		if err != nil {
			return "", fmt.Errorf("upload asset %q returned an invalid asset URL", asset.name)
		}
		extension := path.Ext(parsed.Path)
		validExtension := extension == ".png" || extension == ".jpg" || extension == ".webp" || extension == ".mp4" || extension == ".mov" || extension == ".webm"
		prefix := "/api/code/mr-assets/"
		parts := strings.Split(strings.TrimPrefix(parsed.Path, prefix), "/")
		validPath := strings.HasPrefix(parsed.Path, prefix) && len(parts) == 3 && parts[0] != "" && (o.org == "" || parts[0] == o.org) && parts[1] == o.repo && parts[2] == response.Msg.AssetId+extension
		validScheme := parsed.Scheme == "https" || (parsed.Scheme == "http" && strings.HasPrefix(os.Getenv("DEPOT_API_URL"), "http://"))
		if !validExtension || !validScheme || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || response.Msg.AssetId == "" || !validPath {
			return "", fmt.Errorf("upload asset %q returned an invalid asset URL", asset.name)
		}
		alt := strings.NewReplacer("\\", " ", "[", " ", "]", " ", "\r", " ", "\n", " ").Replace(asset.name)
		result += fmt.Sprintf("\n\n![%s](%s)", alt, response.Msg.Url)
	}
	return result, nil
}
