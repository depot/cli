package ci

import "testing"

func TestParseGitHubRepo(t *testing.T) {
	tests := []struct {
		name      string
		remoteURL string
		want      string
	}{
		{"ssh", "git@github.com:depot/cli.git", "depot/cli"},
		{"ssh no .git", "git@github.com:depot/cli", "depot/cli"},
		{"https", "https://github.com/depot/cli.git", "depot/cli"},
		{"https no .git", "https://github.com/depot/cli", "depot/cli"},
		{"https trailing slash", "https://github.com/depot/cli/", "depot/cli"},
		{"non-github ssh", "git@gitlab.com:depot/cli.git", ""},
		{"non-github https", "https://gitlab.com/depot/cli.git", ""},
		{"bitbucket ssh", "git@bitbucket.org:depot/cli.git", ""},
		{"empty", "", ""},
		{"invalid", "not-a-url", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGitHubRepo(tt.remoteURL)
			if got != tt.want {
				t.Errorf("parseGitHubRepo(%q) = %q, want %q", tt.remoteURL, got, tt.want)
			}
		})
	}
}
