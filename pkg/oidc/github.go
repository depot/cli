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
	"time"
)

const (
	secretMigrationAudiencePrefix  = "https://depot.dev/ci/secret-migration/"
	githubOIDCMaxAttempts          = 3
	githubOIDCRequestTimeout       = 10 * time.Second
	githubOIDCInitialRetryBackoff  = 100 * time.Millisecond
	SecretMigrationBranchPrefix    = "depot-migrate-secrets-"
	SecretMigrationBranchPrefixEnv = "DEPOT_SECRET_MIGRATION_BRANCH_PREFIX"
)

var SecretMigrationIntentIDPattern = regexp.MustCompile(`^[0123456789bcdfghjklmnpqrstvwxz]{10}$`)
var githubOIDCHTTPClient = &http.Client{Timeout: githubOIDCRequestTimeout}

func SecretMigrationIntentIDFromGitHubRef(refName string) string {
	return SecretMigrationIntentIDFromGitHubRefWithPrefix(refName, SecretMigrationBranchPrefix)
}

func SecretMigrationIntentIDFromGitHubRefWithPrefix(refName, branchPrefix string) string {
	if branchPrefix == "" || !strings.HasPrefix(refName, branchPrefix) {
		return ""
	}
	intentID := strings.TrimPrefix(refName, branchPrefix)
	if !SecretMigrationIntentIDPattern.MatchString(intentID) {
		return ""
	}
	return intentID
}

func SecretMigrationIntentIDFromGitHubActionsEnvironment() string {
	if os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN") == "" || os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL") == "" {
		return ""
	}
	branchPrefix := os.Getenv(SecretMigrationBranchPrefixEnv)
	if branchPrefix == "" {
		branchPrefix = SecretMigrationBranchPrefix
	}
	return SecretMigrationIntentIDFromGitHubRefWithPrefix(os.Getenv("GITHUB_REF_NAME"), branchPrefix)
}

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

	var lastErr error
	for attempt := 0; attempt < githubOIDCMaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "GET", requestURL.String(), nil)
		if err != nil {
			return "", err
		}
		req.Header.Add("Authorization", "bearer "+requestToken)

		resp, err := githubOIDCHTTPClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			lastErr = fmt.Errorf("GitHub OIDC token request failed: %w", err)
		} else {
			token, retryable, err := parseGitHubOIDCResponse(resp)
			if err == nil {
				return token, nil
			}
			if !retryable {
				return "", err
			}
			lastErr = err
		}

		if attempt == githubOIDCMaxAttempts-1 {
			return "", lastErr
		}
		if err := waitForGitHubOIDCRetry(ctx, githubOIDCInitialRetryBackoff<<attempt); err != nil {
			return "", err
		}
	}

	return "", lastErr
}

func parseGitHubOIDCResponse(resp *http.Response) (string, bool, error) {
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		err := fmt.Errorf("GitHub OIDC token request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
		retryable := resp.StatusCode == http.StatusTooManyRequests ||
			(resp.StatusCode >= http.StatusInternalServerError && resp.StatusCode < 600)
		return "", retryable, err
	}

	var payload struct {
		Value string `json:"value"`
	}

	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&payload); err != nil {
		return "", false, err
	}
	if payload.Value == "" {
		return "", false, fmt.Errorf("GitHub OIDC token response did not include a token")
	}
	return payload.Value, false, nil
}

func waitForGitHubOIDCRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
