package actionspublic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/depot/cli/pkg/api"
)

const ClaimEndpoint = "https://actions-public-oidc.depot.dev/claim"

func RetrieveToken(ctx context.Context, audience string) (string, error) {
	runID := os.Getenv("GITHUB_RUN_ID")
	eventName := os.Getenv("GITHUB_EVENT_NAME")
	eventPath := os.Getenv("GITHUB_EVENT_PATH")

	// Skip if not running in a GitHub Actions environment
	if runID == "" || eventName == "" || eventPath == "" {
		return "", nil
	}

	// Skip if not a pull_request workflow
	if eventName != "pull_request" {
		return "", nil
	}

	data, err := os.ReadFile(eventPath)
	if err != nil {
		return "", err
	}

	payload := &EventPayload{}
	if err := json.Unmarshal(data, payload); err != nil {
		return "", err
	}

	// Skip if any fields are missing
	if payload.Repository == nil ||
		payload.PullRequest == nil ||
		payload.PullRequest.Head == nil ||
		payload.PullRequest.Head.Repo == nil {
		return "", nil
	}

	// Skip if the the repository is private, or the pull request is from the same repository
	if payload.Repository.Private || payload.PullRequest.Head.Repo.FullName == payload.Repository.FullName {
		return "", nil
	}

	requestBody, err := json.Marshal(&ClaimRequest{
		Aud:       audience,
		EventName: eventName,
		Repo:      payload.Repository.FullName,
		RunID:     runID,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", ClaimEndpoint, bytes.NewBuffer(requestBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", api.Agent())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errorResponse := &ErrorResponse{}
		if err := json.NewDecoder(resp.Body).Decode(errorResponse); err != nil {
			return "", fmt.Errorf("error from claim endpoint: %s", resp.Status)
		}
		return "", fmt.Errorf("error from claim endpoint: %s", errorResponse.Error)
	}

	challengeResponse := &ChallengeResponse{}
	if err := json.NewDecoder(resp.Body).Decode(challengeResponse); err != nil {
		return "", fmt.Errorf("error decoding response from claim endpoint: %s", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		for {
			fmt.Printf("Waiting for OIDC auth challenge %s", challengeResponse.ChallengeCode)

			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
			}
		}
	}()

	for i := 0; i < 60; i++ {
		req, err := http.NewRequestWithContext(ctx, "POST", challengeResponse.ExchangeURL, bytes.NewBuffer([]byte{}))
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", api.Agent())

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == 200 {
			tokenBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				continue
			}
			return string(tokenBytes), nil
		}

		time.Sleep(1 * time.Second)
	}

	return "", fmt.Errorf("OIDC auth challenge %s timed out", challengeResponse.ChallengeCode)
}
