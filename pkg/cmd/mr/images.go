package mr

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

const maxImageBytes = 10 * 1024 * 1024

type localImage struct {
	name    string
	content []byte
}

// Read every file before uploading so a missing later file cannot partially publish the request.
// The API validates the image decoder, format, and pixel limits from these exact bytes.
func readImages(paths []string) ([]localImage, error) {
	if len(paths) > 10 {
		return nil, fmt.Errorf("at most 10 --image files are allowed")
	}
	images := make([]localImage, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("image %q: %w", path, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxImageBytes {
			return nil, fmt.Errorf("image %q must be a regular file containing 1 byte to 10 MiB", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("image %q: %w", path, err)
		}
		content, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
		closeErr := file.Close()
		if err != nil {
			return nil, fmt.Errorf("image %q: %w", path, err)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(content) == 0 || len(content) > maxImageBytes {
			return nil, fmt.Errorf("image %q changed size while reading", path)
		}
		images = append(images, localImage{name: filepath.Base(path), content: content})
	}
	return images, nil
}

func (o *options) uploadImages(cmd *cobra.Command, head, title, body string, images []localImage) (string, error) {
	result := body
	client := api.NewMergeRequestClient()
	for index, image := range images {
		// Each explicit upload is new; transport retries below retain this identity.
		// Looking up an existing MR before uploading also handles a lost create response.
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", err
		}
		hash := sha256.New()
		_, _ = hash.Write(nonce[:])
		for _, value := range []string{head, title, body, fmt.Sprint(index), image.name} {
			_, _ = io.WriteString(hash, value+"\x00")
		}
		_, _ = hash.Write(image.content)
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Uploading image %d/%d: %s\n", index+1, len(images), image.name)
		message := &code.UploadMergeRequestImageRequest{
			RepositoryId: o.repo, UploadKey: hex.EncodeToString(hash.Sum(nil)), Content: image.content,
		}
		var response *connect.Response[code.UploadMergeRequestImageResponse]
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			response, err = client.UploadMergeRequestImage(cmd.Context(), request(o, message))
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
			return "", fmt.Errorf("upload image %q: %w (merge request was not created)", image.name, err)
		}
		parsed, err := url.Parse(response.Msg.Url)
		prefix := "/api/code/mr-images/" + o.org + "/" + o.repo + "/"
		if err != nil || parsed.Scheme != "https" || parsed.Host != "depot.dev" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || response.Msg.ImageId == "" || parsed.Path != prefix+response.Msg.ImageId {
			return "", fmt.Errorf("upload image %q returned an invalid image URL", image.name)
		}
		alt := strings.NewReplacer("\\", " ", "[", " ", "]", " ", "\r", " ", "\n", " ").Replace(image.name)
		result += fmt.Sprintf("\n\n![%s](%s)", alt, response.Msg.Url)
	}
	return result, nil
}
