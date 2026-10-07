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
	if len(policy) == 0 {
		t.Fatal("embedded policy has no labels")
	}
}

func TestParseRunnerPolicyRejectsInvalid(t *testing.T) {
	const source = `"source":{"repository":"depot/api","revision":"0123456789abcdef0123456789abcdef01234567"}`
	cases := map[string]string{
		"schemaVersion":       `{"schemaVersion":2,` + source + `,"labels":[{"label":"depot-a","supported":true}]}`,
		"unknownField":        `{"schemaVersion":1,` + source + `,"labels":[{"label":"depot-a","supported":true,"reason":"no"}]}`,
		"noLabels":            `{"schemaVersion":1,` + source + `,"labels":[]}`,
		"notDepot":            `{"schemaVersion":1,` + source + `,"labels":[{"label":"ubuntu-latest","supported":true}]}`,
		"duplicate":           `{"schemaVersion":1,` + source + `,"labels":[{"label":"depot-a","supported":true},{"label":"depot-a","supported":true}]}`,
		"missingSupported":    `{"schemaVersion":1,` + source + `,"labels":[{"label":"depot-a"}]}`,
		"missingSource":       `{"schemaVersion":1,"labels":[{"label":"depot-a","supported":true}]}`,
		"nullSource":          `{"schemaVersion":1,"source":null,"labels":[{"label":"depot-a","supported":true}]}`,
		"sourceRepository":    `{"schemaVersion":1,"source":{"repository":"depot/cli","revision":"0123456789abcdef0123456789abcdef01234567"},"labels":[{"label":"depot-a","supported":true}]}`,
		"sourceShortRevision": `{"schemaVersion":1,"source":{"repository":"depot/api","revision":"0123abc"},"labels":[{"label":"depot-a","supported":true}]}`,
		"trailingData":        `{"schemaVersion":1,` + source + `,"labels":[{"label":"depot-a","supported":true}]}{}`,
		"trailingBracket":     `{"schemaVersion":1,` + source + `,"labels":[{"label":"depot-a","supported":true}]}]`,
		"trailingBrace":       `{"schemaVersion":1,` + source + `,"labels":[{"label":"depot-a","supported":true}]} }`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRunnerPolicy([]byte(data)); err == nil {
				t.Fatal("expected error")
			}
		})
	}

	valid := `{"schemaVersion":1,` + source + `,"labels":[{"label":"depot-a","supported":false},{"label":"depot-b","supported":true}]}`
	policy, err := parseRunnerPolicy([]byte(strings.TrimSpace(valid)))
	if err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	if policy["depot-a"] || !policy["depot-b"] {
		t.Fatalf("policy = %v", policy)
	}
	if _, err := parseRunnerPolicy([]byte(valid + "\n \t\n")); err != nil {
		t.Fatalf("valid policy with trailing whitespace rejected: %v", err)
	}
}
