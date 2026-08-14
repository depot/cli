package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// SecretMigrationIntentIDEnv carries the per-run intent selected by a migration workflow.
const SecretMigrationIntentIDEnv = "DEPOT_SECRET_MIGRATION_INTENT_ID"

const secretMigrationAudiencePrefix = "https://depot.dev/ci/secret-migration/"

var SecretMigrationIntentIDPattern = regexp.MustCompile(`^[0123456789bcdfghjklmnpqrstvwxz]{10}$`)

type GitHubOIDCProvider struct {
}

func NewGitHubOIDCProvider() *GitHubOIDCProvider {
	return &GitHubOIDCProvider{}
}

func (p *GitHubOIDCProvider) Name() string {
	return "github"
}

func (p *GitHubOIDCProvider) RetrieveToken(ctx context.Context) (string, error) {
	return p.retrieveToken(ctx, audience)
}

func (p *GitHubOIDCProvider) RetrieveSecretMigrationToken(ctx context.Context, intentID string) (string, error) {
	if !SecretMigrationIntentIDPattern.MatchString(intentID) {
		return "", fmt.Errorf("invalid secret migration intent ID")
	}
	return p.retrieveToken(ctx, secretMigrationAudiencePrefix+intentID)
}

func (p *GitHubOIDCProvider) retrieveToken(ctx context.Context, tokenAudience string) (string, error) {
	requestToken := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if requestToken == "" {
		return "", nil
	}

	requestURLValue := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
	if requestURLValue == "" {
		return "", nil
	}

	requestURL, err := url.Parse(requestURLValue)
	if err != nil {
		return "", err
	}
	query := requestURL.Query()
	query.Set("audience", tokenAudience)
	requestURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", requestURL.String(), nil)
	if err != nil {
		return "", err
	}

	req.Header.Add("Authorization", "bearer "+requestToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("GitHub OIDC token request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Value string `json:"value"`
	}

	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&payload); err != nil {
		return "", err
	}
	if payload.Value == "" {
		return "", fmt.Errorf("GitHub OIDC token response did not include a token")
	}
	return payload.Value, nil
}
