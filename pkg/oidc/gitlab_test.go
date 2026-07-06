package oidc

import (
	"context"
	"testing"
)

func TestGitLabOIDCProviderRetrieveToken(t *testing.T) {
	t.Setenv("GITLAB_CI", "true")
	t.Setenv(gitlabOIDCTokenEnv, "gitlab-oidc-token")

	token, err := NewGitLabOIDCProvider().RetrieveToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "gitlab-oidc-token" {
		t.Fatalf("token = %q, want %q", token, "gitlab-oidc-token")
	}
}

func TestGitLabOIDCProviderSkipsOutsideGitLabCI(t *testing.T) {
	t.Setenv("GITLAB_CI", "")
	t.Setenv(gitlabOIDCTokenEnv, "gitlab-oidc-token")

	token, err := NewGitLabOIDCProvider().RetrieveToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		t.Fatalf("token = %q, want empty", token)
	}
}
