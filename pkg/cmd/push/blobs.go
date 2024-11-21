package push

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
)

type BlobToPush struct {
	ParsedTag     *ParsedTag
	RegistryToken *Token
	BuildID       string
	Digest        digest.Digest
}

// PushBlob requests a blob to be pushed from Depot to a destination registry.
func PushBlob(ctx context.Context, depotToken string, blob *BlobToPush) error {
	var err error
	var req *http.Request

	pushRequest := struct {
		RegistryHost        string `json:"registryHost"`
		RepositoryNamespace string `json:"repositoryNamespace"`
		RegistryToken       string `json:"registryToken"`
		TokenScheme         string `json:"tokenScheme"`
	}{
		RegistryHost:        blob.ParsedTag.Host,
		RepositoryNamespace: blob.ParsedTag.Path,
		RegistryToken:       blob.RegistryToken.Token,
		TokenScheme:         blob.RegistryToken.Scheme,
	}
	buf, _ := json.MarshalIndent(pushRequest, "", "  ")
	url := fmt.Sprintf("https://blob.depot.dev/blobs/%s/%s", blob.BuildID, blob.Digest.String())

	attempts := 0
	for {
		attempts += 1

		req, err = http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(buf)))
		if err != nil {
			return err
		}
		req.Header.Add("Authorization", "Bearer "+depotToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()

		if resp.StatusCode/100 != 2 {
			if resp.StatusCode >= 500 && attempts < 3 {
				time.Sleep(5 * time.Second)
				continue
			}

			body, _ := io.ReadAll(resp.Body)
			err := fmt.Errorf("unexpected status code: %d %s", resp.StatusCode, string(body))
			return err
		}

		return nil
	}

}
