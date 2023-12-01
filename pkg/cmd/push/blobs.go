package push

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/opencontainers/go-digest"
)

type BlobRequest struct {
	ParsedTag     *ParsedTag
	RegistryToken *Token
	BuildID       string
	Digest        digest.Digest
}

// PushBlob requests a blob to be pushed from Depot to a destination registry.
func PushBlob(ctx context.Context, depotToken string, req *BlobRequest) error {
	pushRequest := struct {
		RegistryHost        string `json:"registryHost"`
		RepositoryNamespace string `json:"repositoryNamespace"`
		RegistryToken       string `json:"registryToken"`
		TokenScheme         string `json:"tokenScheme"`
	}{
		RegistryHost:        req.ParsedTag.Host,
		RepositoryNamespace: req.ParsedTag.Path,
		RegistryToken:       req.RegistryToken.Token,
		TokenScheme:         req.RegistryToken.Scheme,
	}
	buf, _ := json.MarshalIndent(pushRequest, "", "  ")

	url := fmt.Sprintf("https://blob.depot.dev/blobs/%s/%s", req.BuildID, req.Digest.String())
	pushReq, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(buf)))
	if err != nil {
		return err
	}
	pushReq.Header.Add("Authorization", "Bearer "+depotToken)
	pushReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(pushReq)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("unexpected status code: %d %s", resp.StatusCode, string(body))
		return err
	}

	return nil
}
