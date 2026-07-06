package oidc

import (
	"context"
	"os"
)

const gitlabOIDCTokenEnv = "DEPOT_OIDC_TOKEN"

type GitLabOIDCProvider struct {
}

func NewGitLabOIDCProvider() *GitLabOIDCProvider {
	return &GitLabOIDCProvider{}
}

func (p *GitLabOIDCProvider) Name() string {
	return "gitlab"
}

func (p *GitLabOIDCProvider) RetrieveToken(ctx context.Context) (string, error) {
	if os.Getenv("GITLAB_CI") == "" {
		return "", nil
	}
	return os.Getenv(gitlabOIDCTokenEnv), nil
}
