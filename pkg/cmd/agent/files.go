package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/docker/go-units"
)

type localFile struct {
	path       string
	attachment *agentv1.DepotAgentAttachment
}

// uploadAttachments hashes each file, uploads only the contents Depot does not already store,
// and returns the attachments to send with a message.
// A file over the server's limit fails the whole call before anything is uploaded.
func uploadAttachments(ctx context.Context, s *session, paths []string, notices io.Writer) ([]*agentv1.DepotAgentAttachment, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	files := make([]localFile, 0, len(paths))
	attachments := make([]*agentv1.DepotAgentAttachment, 0, len(paths))
	for _, path := range paths {
		file, err := hashFile(path)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(notices, "(attaching %s, %s)\n", file.attachment.GetName(), units.HumanSize(float64(file.attachment.GetSizeBytes())))
		files = append(files, file)
		attachments = append(attachments, file.attachment)
	}

	resp, err := withRetries(ctx, func() (*connect.Response[agentv1.PrepareAttachmentUploadsResponse], error) {
		return s.client.PrepareAttachmentUploads(ctx, authed(s, &agentv1.PrepareAttachmentUploadsRequest{Attachments: attachments}))
	})
	if err != nil {
		return nil, fmt.Errorf("prepare attachments: %w", err)
	}
	if limit := resp.Msg.GetMaxAttachmentBytes(); limit > 0 {
		for _, file := range files {
			if size := file.attachment.GetSizeBytes(); size > limit {
				return nil, fmt.Errorf("%s is %s, over the %s limit for attachments", file.path, units.HumanSize(float64(size)), units.HumanSize(float64(limit)))
			}
		}
	}

	bySHA := make(map[string]localFile, len(files))
	for _, file := range files {
		bySHA[file.attachment.GetSha256()] = file
	}
	for _, upload := range resp.Msg.GetUploads() {
		file, ok := bySHA[upload.GetSha256()]
		if !ok {
			return nil, fmt.Errorf("prepare attachments: server asked for unknown file %s", upload.GetSha256())
		}
		if err := putFile(ctx, file, upload); err != nil {
			return nil, err
		}
	}
	return attachments, nil
}

func hashFile(path string) (localFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return localFile{}, fmt.Errorf("attach %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return localFile{}, fmt.Errorf("attach %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return localFile{}, fmt.Errorf("attach %s: not a regular file", path)
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return localFile{}, fmt.Errorf("attach %s: %w", path, err)
	}
	attachment := &agentv1.DepotAgentAttachment{
		Sha256:    hex.EncodeToString(h.Sum(nil)),
		SizeBytes: uint64(size),
		Name:      filepath.Base(path),
	}
	if mediaType := mime.TypeByExtension(filepath.Ext(path)); mediaType != "" {
		attachment.MediaType = ptr(mediaType)
	}
	return localFile{path: path, attachment: attachment}, nil
}

func putFile(ctx context.Context, file localFile, upload *agentv1.DepotAgentAttachmentUpload) error {
	f, err := os.Open(file.path)
	if err != nil {
		return fmt.Errorf("upload %s: %w", file.path, err)
	}
	defer f.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, upload.GetUrl(), f)
	if err != nil {
		return fmt.Errorf("upload %s: %w", file.path, err)
	}
	req.ContentLength = int64(file.attachment.GetSizeBytes())
	for k, v := range upload.GetHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload %s: %w", file.path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("upload %s: %s: %s", file.path, resp.Status, body)
	}
	return nil
}
