package ci

import (
	"regexp"
	"testing"
)

func TestNewCmdRun_RefFlagRegistered(t *testing.T) {
	cmd := NewCmdRun()
	f := cmd.Flags().Lookup("ref")
	if f == nil {
		t.Fatal("expected --ref flag to be registered")
	}
	if f.DefValue != "" {
		t.Errorf("expected --ref default to be empty, got %q", f.DefValue)
	}
}

func TestNewCmdRun_ShaFlagRegistered(t *testing.T) {
	cmd := NewCmdRun()
	f := cmd.Flags().Lookup("sha")
	if f == nil {
		t.Fatal("expected --sha flag to be registered")
	}
	if f.DefValue != "" {
		t.Errorf("expected --sha default to be empty, got %q", f.DefValue)
	}
}

func TestNewCmdRun_RefAndShaMutuallyExclusive(t *testing.T) {
	cmd := NewCmdRun()
	// Set both flags and a workflow so the command doesn't short-circuit to help
	cmd.SetArgs([]string{"--workflow", "test.yml", "--ref", "my-branch", "--sha", "abcdef1234567890abcdef1234567890abcdef12"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when both --ref and --sha are set")
	}
	if !regexp.MustCompile(`(?i)mutually exclusive`).MatchString(err.Error()) {
		t.Errorf("expected 'mutually exclusive' error, got: %s", err.Error())
	}
}

func TestNewCmdRun_ShaValidation(t *testing.T) {
	cmd := NewCmdRun()
	cmd.SetArgs([]string{"--workflow", "test.yml", "--sha", "not-a-valid-sha"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid --sha value")
	}
	if !regexp.MustCompile(`(?i)40.char|hex|invalid`).MatchString(err.Error()) {
		t.Errorf("expected SHA validation error, got: %s", err.Error())
	}
}

func TestIsValidSHA(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"abcdef1234567890abcdef1234567890abcdef12", true},
		{"ABCDEF1234567890ABCDEF1234567890ABCDEF12", true},
		{"not-a-sha", false},
		{"abc123", false},
		{"", false},
		{"abcdef1234567890abcdef1234567890abcdef12X", false}, // 41 chars
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isValidSHA(tt.input); got != tt.want {
				t.Errorf("isValidSHA(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
