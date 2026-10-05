package compat

import (
	"strings"
	"testing"
)

func TestEmbeddedRunnerPolicy(t *testing.T) {
	policy, err := parseRunnerPolicy(runnerPolicyJSON)
	if err != nil {
		t.Fatalf("embedded policy is invalid: %v", err)
	}
	for label, want := range map[string]bool{
		"depot-ubuntu-latest":    true,
		"depot-ubuntu-latest-96": false,
		"depot-macos-latest":     false,
		"depot-windows-latest":   false,
	} {
		if got, ok := policy[label]; !ok || got != want {
			t.Errorf("policy[%q] = %v, %v; want %v", label, got, ok, want)
		}
	}
}

func TestParseRunnerPolicyRejectsInvalid(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	cases := map[string]string{
		"schemaVersion":       `{"schemaVersion":2,"labels":[{"label":"depot-a","supported":true}]}`,
		"unknownField":        `{"schemaVersion":1,"labels":[{"label":"depot-a","supported":true,"cpus":2}]}`,
		"noLabels":            `{"schemaVersion":1,"labels":[]}`,
		"notDepot":            `{"schemaVersion":1,"labels":[{"label":"ubuntu-latest","supported":true}]}`,
		"duplicate":           `{"schemaVersion":1,"labels":[{"label":"depot-a","supported":true},{"label":"depot-a","supported":true}]}`,
		"missingSupported":    `{"schemaVersion":1,"labels":[{"label":"depot-a"}]}`,
		"unsupportedNoReason": `{"schemaVersion":1,"labels":[{"label":"depot-a","supported":false}]}`,
		"supportedWithReason": `{"schemaVersion":1,"labels":[{"label":"depot-a","supported":true,"reason":"no"}]}`,
		"sourceRepository":    `{"schemaVersion":1,"source":{"repository":"depot/cli","revision":"` + revision + `"},"labels":[{"label":"depot-a","supported":true}]}`,
		"sourceShortRevision": `{"schemaVersion":1,"source":{"repository":"depot/api","revision":"0123abc"},"labels":[{"label":"depot-a","supported":true}]}`,
		"trailingData":        `{"schemaVersion":1,"labels":[{"label":"depot-a","supported":true}]}{}`,
		"trailingBracket":     `{"schemaVersion":1,"labels":[{"label":"depot-a","supported":true}]}]`,
		"trailingBrace":       `{"schemaVersion":1,"labels":[{"label":"depot-a","supported":true}]} }`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRunnerPolicy([]byte(data)); err == nil {
				t.Fatal("expected error")
			}
		})
	}

	valid := `{"schemaVersion":1,"source":{"repository":"depot/api","revision":"` + revision + `"},"labels":[{"label":"depot-a","supported":false,"reason":"no"}]}`
	if _, err := parseRunnerPolicy([]byte(strings.TrimSpace(valid))); err != nil {
		t.Fatalf("valid policy with source rejected: %v", err)
	}
	if _, err := parseRunnerPolicy([]byte(valid + "\n \t\n")); err != nil {
		t.Fatalf("valid policy with trailing whitespace rejected: %v", err)
	}
}
