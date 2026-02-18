package migrate

import (
	"encoding/json"
	"testing"
)

func TestWorkflowFileJSONRoundTrip(t *testing.T) {
	workflow := WorkflowFile{
		Path:     ".github/workflows/test.yml",
		Name:     "Test",
		Triggers: []string{"push", "pull_request"},
		Jobs: []JobInfo{
			{
				Name:         "test",
				RunsOn:       "ubuntu-latest",
				HasMatrix:    true,
				HasContainer: false,
				HasServices:  false,
			},
		},
		Secrets:   []string{"GITHUB_TOKEN"},
		Variables: []string{"NODE_VERSION"},
	}

	data, err := json.Marshal(workflow)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded WorkflowFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Path != workflow.Path || decoded.Name != workflow.Name ||
		len(decoded.Triggers) != len(workflow.Triggers) ||
		len(decoded.Jobs) != len(workflow.Jobs) {
		t.Errorf("Round-trip failed: got %+v, want %+v", decoded, workflow)
	}
}

func TestJobInfoJSONRoundTrip(t *testing.T) {
	job := JobInfo{
		Name:         "build",
		RunsOn:       "ubuntu-latest",
		UsesReusable: "owner/repo/.github/workflows/reusable.yml",
		HasMatrix:    true,
		HasContainer: true,
		HasServices:  true,
	}

	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded JobInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Name != job.Name || decoded.RunsOn != job.RunsOn ||
		decoded.UsesReusable != job.UsesReusable || decoded.HasMatrix != job.HasMatrix ||
		decoded.HasContainer != job.HasContainer || decoded.HasServices != job.HasServices {
		t.Errorf("Round-trip failed: got %+v, want %+v", decoded, job)
	}
}

func TestMigrationPlanJSONRoundTrip(t *testing.T) {
	plan := MigrationPlan{
		Workflows: []*WorkflowFile{
			{
				Path:      ".github/workflows/test.yml",
				Name:      "Test",
				Triggers:  []string{"push"},
				Jobs:      []JobInfo{},
				Secrets:   []string{"TOKEN"},
				Variables: []string{"VERSION"},
			},
		},
		DetectedSecrets:   []string{"GITHUB_TOKEN", "NPM_TOKEN"},
		DetectedVariables: []string{"NODE_VERSION", "REGISTRY_URL"},
	}

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded MigrationPlan
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(decoded.Workflows) != len(plan.Workflows) ||
		len(decoded.DetectedSecrets) != len(plan.DetectedSecrets) ||
		len(decoded.DetectedVariables) != len(plan.DetectedVariables) {
		t.Errorf("Round-trip failed: got %+v, want %+v", decoded, plan)
	}
}

func TestMigrationResultJSONRoundTrip(t *testing.T) {
	result := MigrationResult{
		FilesCopied:       []string{".github/workflows/test.yml"},
		SecretsConfigured: []string{"GITHUB_TOKEN"},
		Warnings:          []string{"Some features not supported"},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded MigrationResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(decoded.FilesCopied) != len(result.FilesCopied) ||
		len(decoded.SecretsConfigured) != len(result.SecretsConfigured) ||
		len(decoded.Warnings) != len(result.Warnings) {
		t.Errorf("Round-trip failed: got %+v, want %+v", decoded, result)
	}
}
