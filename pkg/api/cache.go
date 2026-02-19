package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"
	cachev1 "github.com/depot/cli/pkg/proto/depot/cache/v1"
	"github.com/depot/cli/pkg/proto/depot/cache/v1/cachev1connect"
)

const defaultCacheBaseURL = "https://cache.depot.dev"

func getCacheBaseURL() string {
	if u := os.Getenv("DEPOT_CACHE_HOST"); u != "" {
		return u
	}
	return defaultCacheBaseURL
}

func newCacheServiceClient() cachev1connect.CacheServiceClient {
	baseURL := getCacheBaseURL()
	return cachev1connect.NewCacheServiceClient(getHTTPClient(baseURL), baseURL, WithUserAgent())
}

// UploadCacheEntry uploads content to the Depot Cache service as a generic entry.
// It uses a 3-step process: CreateEntry, HTTP PUT to presigned URL, FinalizeEntry.
// If the entry already exists (content-addressed), it returns nil.
func UploadCacheEntry(ctx context.Context, token, key string, content []byte) error {
	client := newCacheServiceClient()

	// Step 1: Create cache entry
	createResp, err := client.CreateEntry(ctx, WithAuthentication(connect.NewRequest(&cachev1.CreateEntryRequest{
		EntryType: "generic",
		Key:       key,
	}), token))
	if err != nil {
		// Content-addressed: if already exists, skip upload
		if connect.CodeOf(err) == connect.CodeAlreadyExists {
			return nil
		}
		return fmt.Errorf("failed to create cache entry: %w", err)
	}

	urls := createResp.Msg.UploadPartUrls
	if len(urls) == 0 {
		return fmt.Errorf("no upload URLs returned from cache service")
	}

	// Step 2: Upload content to presigned S3 URL
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, urls[0], bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("failed to create upload request: %w", err)
	}
	req.ContentLength = int64(len(content))

	uploadResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload content: %w", err)
	}
	defer uploadResp.Body.Close()

	if uploadResp.StatusCode != http.StatusOK {
		return fmt.Errorf("upload failed with status %d", uploadResp.StatusCode)
	}

	etag := strings.Trim(uploadResp.Header.Get("ETag"), "\"")
	if etag == "" {
		return fmt.Errorf("no ETag returned from upload")
	}

	// Step 3: Finalize the entry
	_, err = client.FinalizeEntry(ctx, WithAuthentication(connect.NewRequest(&cachev1.FinalizeEntryRequest{
		EntryId:         createResp.Msg.EntryId,
		SizeBytes:       int64(len(content)),
		UploadPartEtags: []string{etag},
	}), token))
	if err != nil {
		return fmt.Errorf("failed to finalize cache entry: %w", err)
	}

	return nil
}
