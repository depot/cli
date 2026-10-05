package compat

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
)

// runnerPolicyJSON is Depot CI's default runs-on label support, generated in
// depot/api and synced here. Do not edit it by hand.
//
//go:embed ci-runner-policy.json
var runnerPolicyJSON []byte

// runnerPolicy maps each exact, case-sensitive Depot runs-on label to whether
// Depot CI can dispatch it under the default label mapping. Organization
// overrides and rollouts stay backend-owned, so the backend remains
// authoritative for a specific job.
var runnerPolicy = sync.OnceValue(func() map[string]bool {
	policy, err := parseRunnerPolicy(runnerPolicyJSON)
	if err != nil {
		panic(fmt.Sprintf("invalid embedded ci-runner-policy.json: %v", err))
	}
	return policy
})

type runnerPolicyFile struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Source        *runnerPolicySource `json:"source"`
	Labels        []runnerPolicyLabel `json:"labels"`
}

// runnerPolicySource records the depot/api commit the sync job copied the
// policy from.
type runnerPolicySource struct {
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
}

type runnerPolicyLabel struct {
	Label     string `json:"label"`
	Supported *bool  `json:"supported"`
	Reason    string `json:"reason"`
}

var gitRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)

func parseRunnerPolicy(data []byte) (map[string]bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var file runnerPolicyFile
	if err := decoder.Decode(&file); err != nil {
		return nil, err
	}
	// More reports only whether an array or object continues, so it misses a
	// stray ] or }; a second Decode must hit EOF instead.
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("unexpected data after policy object")
	}
	if file.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported schemaVersion %d", file.SchemaVersion)
	}
	if source := file.Source; source != nil {
		if source.Repository != "depot/api" {
			return nil, fmt.Errorf("unexpected source repository %q", source.Repository)
		}
		if !gitRevision.MatchString(source.Revision) {
			return nil, fmt.Errorf("source revision %q is not a full commit SHA", source.Revision)
		}
	}
	if len(file.Labels) == 0 {
		return nil, errors.New("no labels")
	}

	policy := make(map[string]bool, len(file.Labels))
	for _, entry := range file.Labels {
		if !strings.HasPrefix(entry.Label, "depot-") {
			return nil, fmt.Errorf("label %q does not start with depot-", entry.Label)
		}
		if _, seen := policy[entry.Label]; seen {
			return nil, fmt.Errorf("duplicate label %q", entry.Label)
		}
		if entry.Supported == nil {
			return nil, fmt.Errorf("label %q is missing supported", entry.Label)
		}
		if *entry.Supported == (entry.Reason != "") {
			return nil, fmt.Errorf("label %q must have a reason only when unsupported", entry.Label)
		}
		policy[entry.Label] = *entry.Supported
	}

	return policy, nil
}
