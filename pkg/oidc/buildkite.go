package oidc

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/buildkite/agent/v3/api"
	"github.com/buildkite/agent/v3/logger"
)

type BuildkiteOIDCProvider struct {
}

func NewBuildkiteOIDCProvider() *BuildkiteOIDCProvider {
	return &BuildkiteOIDCProvider{}
}

func (p *BuildkiteOIDCProvider) RetrieveToken(ctx context.Context) (string, error) {
	agentToken := os.Getenv("BUILDKITE_AGENT_ACCESS_TOKEN")
	if agentToken == "" {
		return "", nil
	}

	endpoint := os.Getenv("BUILDKITE_AGENT_ENDPOINT")
	if endpoint == "" {
		endpoint = "https://agent.buildkite.com/v3"
	}

	jobID := os.Getenv("BUILDKITE_JOB_ID")

	logLevel := os.Getenv("BUILDKITE_AGENT_LOG_LEVEL")
	if logLevel == "" {
		logLevel = "notice"
	}

	l := logger.NewConsoleLogger(logger.NewTextPrinter(os.Stderr), os.Exit)
	level, err := logger.LevelFromString(logLevel)
	if err != nil {
		return "", err
	}
	l.SetLevel(level)

	client := api.NewClient(l, api.Config{Token: agentToken, Endpoint: endpoint})
	token, response, err := client.OIDCToken(ctx, &api.OIDCTokenRequest{Audience: audience, Job: jobID})
	if err != nil {
		return "", err
	}
	if response != nil && response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("buildkite agent request failed with status: %s", response.Status)
	}
	return token.Token, nil
}
