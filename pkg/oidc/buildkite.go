package oidc

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/depot/cli/pkg/oidc/buildkite"
)

type BuildkiteOIDCProvider struct {
}

func NewBuildkiteOIDCProvider() *BuildkiteOIDCProvider {
	return &BuildkiteOIDCProvider{}
}

func (p *BuildkiteOIDCProvider) Name() string {
	return "buildkite"
}

func (p *BuildkiteOIDCProvider) RetrieveToken(ctx context.Context) (string, error) {
	agentToken := os.Getenv("BUILDKITE_AGENT_ACCESS_TOKEN")
	if agentToken == "" {
		return "", fmt.Errorf("Not running in a Buildkite agent environment")
	}

	endpoint := os.Getenv("BUILDKITE_AGENT_ENDPOINT")
	if endpoint == "" {
		endpoint = "https://agent.buildkite.com/v3"
	}

	jobID := os.Getenv("BUILDKITE_JOB_ID")

	client := buildkite.NewClient(buildkite.Config{Token: agentToken, Endpoint: endpoint})
	token, response, err := client.OIDCToken(ctx, &buildkite.OIDCTokenRequest{Audience: audience, Job: jobID})
	if err != nil {
		return "", err
	}
	if response != nil && response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("buildkite agent request failed with status: %s", response.Status)
	}
	return token.Token, nil
}
