package version

import (
	"testing"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		buildDate string
		want      string
	}{
		{
			name:      "version with v prefix",
			version:   "v1.2.3",
			buildDate: "2023-01-01",
			want:      "depot version 1.2.3 (2023-01-01)\nhttps://github.com/depot/cli/releases/tag/v1.2.3\n",
		},
		{
			name:      "version without v prefix",
			version:   "1.2.3",
			buildDate: "2023-01-01",
			want:      "depot version 1.2.3 (2023-01-01)\nhttps://github.com/depot/cli/releases/tag/v1.2.3\n",
		},
		{
			name:      "version without build date",
			version:   "1.2.3",
			buildDate: "",
			want:      "depot version 1.2.3\nhttps://github.com/depot/cli/releases/tag/v1.2.3\n",
		},
		{
			name:      "dev version",
			version:   "0.0.0-dev",
			buildDate: "",
			want:      "depot version 0.0.0-dev\nhttps://github.com/depot/cli/releases/latest\n",
		},
		{
			name:      "prerelease version",
			version:   "1.2.3-beta.1",
			buildDate: "2023-01-01",
			want:      "depot version 1.2.3-beta.1 (2023-01-01)\nhttps://github.com/depot/cli/releases/tag/v1.2.3-beta.1\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Format(tt.version, tt.buildDate)
			if got != tt.want {
				t.Errorf("Format() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChangelogURL(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{
			name:    "stable version",
			version: "1.2.3",
			want:    "https://github.com/depot/cli/releases/tag/v1.2.3",
		},
		{
			name:    "stable version with v prefix",
			version: "v1.2.3",
			want:    "https://github.com/depot/cli/releases/tag/v1.2.3",
		},
		{
			name:    "prerelease version",
			version: "1.2.3-beta.1",
			want:    "https://github.com/depot/cli/releases/tag/v1.2.3-beta.1",
		},
		{
			name:    "dev version",
			version: "0.0.0-dev",
			want:    "https://github.com/depot/cli/releases/latest",
		},
		{
			name:    "invalid version",
			version: "invalid",
			want:    "https://github.com/depot/cli/releases/latest",
		},
		{
			name:    "empty version",
			version: "",
			want:    "https://github.com/depot/cli/releases/latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := changelogURL(tt.version)
			if got != tt.want {
				t.Errorf("changelogURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
