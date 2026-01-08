package claude

import (
	"testing"
)

func TestIsGitURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "https URL",
			input: "https://github.com/user/repo.git",
			want:  true,
		},
		{
			name:  "https URL without .git suffix",
			input: "https://github.com/user/repo",
			want:  true,
		},
		{
			name:  "http URL",
			input: "http://github.com/user/repo.git",
			want:  true,
		},
		{
			name:  "git@ URL",
			input: "git@github.com:user/repo.git",
			want:  true,
		},
		{
			name:  "ssh:// URL",
			input: "ssh://git@github.com/user/repo.git",
			want:  true,
		},
		{
			name:  "URL containing .git",
			input: "example.com/repo.git",
			want:  true,
		},
		{
			name:  "local path",
			input: "/home/user/repo",
			want:  false,
		},
		{
			name:  "relative path",
			input: "./repo",
			want:  false,
		},
		{
			name:  "plain name",
			input: "repo-name",
			want:  false,
		},
		{
			name:  "empty string",
			input: "",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isGitURL(tt.input)
			if got != tt.want {
				t.Errorf("isGitURL(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseGitURL(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantURL    string
		wantBranch string
	}{
		{
			name:       "URL without branch",
			input:      "https://github.com/user/repo.git",
			wantURL:    "https://github.com/user/repo.git",
			wantBranch: "main",
		},
		{
			name:       "URL with branch",
			input:      "https://github.com/user/repo.git#develop",
			wantURL:    "https://github.com/user/repo.git",
			wantBranch: "develop",
		},
		{
			name:       "URL with feature branch",
			input:      "https://github.com/user/repo.git#feature/new-feature",
			wantURL:    "https://github.com/user/repo.git",
			wantBranch: "feature/new-feature",
		},
		{
			name:       "git@ URL with branch",
			input:      "git@github.com:user/repo.git#main",
			wantURL:    "git@github.com:user/repo.git",
			wantBranch: "main",
		},
		{
			name:       "empty branch after hash",
			input:      "https://github.com/user/repo.git#",
			wantURL:    "https://github.com/user/repo.git",
			wantBranch: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotBranch := parseGitURL(tt.input)
			if gotURL != tt.wantURL {
				t.Errorf("parseGitURL(%q) URL = %q, want %q", tt.input, gotURL, tt.wantURL)
			}
			if gotBranch != tt.wantBranch {
				t.Errorf("parseGitURL(%q) branch = %q, want %q", tt.input, gotBranch, tt.wantBranch)
			}
		})
	}
}

func TestShellEscapeArg(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple word",
			input: "hello",
			want:  "hello",
		},
		{
			name:  "empty string",
			input: "",
			want:  "''",
		},
		{
			name:  "string with space",
			input: "hello world",
			want:  "'hello world'",
		},
		{
			name:  "string with single quote",
			input: "it's",
			want:  "'it'\"'\"'s'",
		},
		{
			name:  "string with double quote",
			input: `say "hello"`,
			want:  `'say "hello"'`,
		},
		{
			name:  "string with shell special characters",
			input: "foo$bar",
			want:  "'foo$bar'",
		},
		{
			name:  "string with backticks",
			input: "foo`bar`",
			want:  "'foo`bar`'",
		},
		{
			name:  "string with newline",
			input: "foo\nbar",
			want:  "'foo\nbar'",
		},
		{
			name:  "string with tabs",
			input: "foo\tbar",
			want:  "'foo\tbar'",
		},
		{
			name:  "hyphenated flag",
			input: "--model",
			want:  "--model",
		},
		{
			name:  "path without spaces",
			input: "/home/user/file.txt",
			want:  "/home/user/file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellEscapeArg(tt.input)
			if got != tt.want {
				t.Errorf("shellEscapeArg(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestShellEscapeArgs(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  string
	}{
		{
			name:  "empty slice",
			input: []string{},
			want:  "",
		},
		{
			name:  "single arg",
			input: []string{"hello"},
			want:  "hello",
		},
		{
			name:  "multiple simple args",
			input: []string{"--model", "claude-3"},
			want:  "--model claude-3",
		},
		{
			name:  "args with spaces",
			input: []string{"-p", "hello world"},
			want:  "-p 'hello world'",
		},
		{
			name:  "complex args",
			input: []string{"--message", "it's a test", "--flag"},
			want:  "--message 'it'\"'\"'s a test' --flag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellEscapeArgs(tt.input)
			if got != tt.want {
				t.Errorf("shellEscapeArgs(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestConvertPathToProjectName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple path",
			input: "/Users/billy/Work",
			want:  "-Users-billy-Work",
		},
		{
			name:  "path with dotfiles",
			input: "/Users/jacobwgillespie/.dotfiles",
			want:  "-Users-jacobwgillespie--dotfiles",
		},
		{
			name:  "path with underscores",
			input: "/home/user/my_project",
			want:  "-home-user-my-project",
		},
		{
			name:  "path with numbers",
			input: "/home/user123/project456",
			want:  "-home-user123-project456",
		},
		{
			name:  "path with trailing slash",
			input: "/home/user/project/",
			want:  "-home-user-project",
		},
		{
			name:  "path with multiple consecutive dots",
			input: "/home/user/../project",
			want:  "-home-project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertPathToProjectName(tt.input)
			if got != tt.want {
				t.Errorf("convertPathToProjectName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractSummaryFromSession(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{
			name:  "empty data",
			input: []byte{},
			want:  "",
		},
		{
			name:  "no summary entry",
			input: []byte(`{"type":"message","content":"hello"}` + "\n" + `{"type":"response","content":"world"}`),
			want:  "",
		},
		{
			name:  "single summary entry",
			input: []byte(`{"type":"message","content":"hello"}` + "\n" + `{"type":"summary","summary":"Test session summary"}`),
			want:  "Test session summary",
		},
		{
			name:  "multiple summaries returns last one",
			input: []byte(`{"type":"summary","summary":"First summary"}` + "\n" + `{"type":"message","content":"work"}` + "\n" + `{"type":"summary","summary":"Second summary"}`),
			want:  "Second summary",
		},
		{
			name:  "summary with special characters",
			input: []byte(`{"type":"summary","summary":"Summary with \"quotes\" and 'apostrophes'"}`),
			want:  `Summary with "quotes" and 'apostrophes'`,
		},
		{
			name:  "invalid JSON lines are skipped",
			input: []byte(`invalid json` + "\n" + `{"type":"summary","summary":"Valid summary"}`),
			want:  "Valid summary",
		},
		{
			name:  "empty lines are skipped",
			input: []byte(`{"type":"summary","summary":"Test"}` + "\n" + "\n" + "  \n"),
			want:  "Test",
		},
		{
			name:  "summary without summary field",
			input: []byte(`{"type":"summary"}`),
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSummaryFromSession(tt.input)
			if got != tt.want {
				t.Errorf("extractSummaryFromSession() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseInt(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{
			name:    "valid positive integer",
			input:   "42",
			want:    42,
			wantErr: false,
		},
		{
			name:    "valid zero",
			input:   "0",
			want:    0,
			wantErr: false,
		},
		{
			name:    "valid negative integer",
			input:   "-10",
			want:    -10,
			wantErr: false,
		},
		{
			name:    "invalid string",
			input:   "abc",
			want:    0,
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			want:    0,
			wantErr: true,
		},
		{
			name:    "float value",
			input:   "3.14",
			want:    3,
			wantErr: false, // Sscanf will parse up to the decimal
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseInt(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseInt(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseInt(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
