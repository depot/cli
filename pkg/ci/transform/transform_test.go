package transform

import (
	"strings"
	"testing"

	"github.com/depot/cli/pkg/ci/compat"
	"github.com/depot/cli/pkg/ci/migrate"
)

func TestTransformWorkflow_NoChanges(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs: []migrate.JobInfo{
			{Name: "build", RunsOn: "depot-ubuntu-latest"},
		},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Changes) != 0 {
		t.Errorf("expected no changes, got %d: %v", len(result.Changes), result.Changes)
	}

	content := string(result.Content)
	if !strings.Contains(content, "No changes were necessary") {
		t.Errorf("expected 'No changes were necessary' in header, got:\n%s", content)
	}

	if !strings.Contains(content, "runs-on: depot-ubuntu-latest") {
		t.Errorf("expected depot-ubuntu-latest preserved, got:\n%s", content)
	}
}

func TestTransformWorkflow_StandardGitHubLabel(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs: []migrate.JobInfo{
			{Name: "build", RunsOn: "ubuntu-latest"},
		},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, "runs-on: depot-ubuntu-latest") {
		t.Errorf("expected depot-ubuntu-latest, got:\n%s", content)
	}
	if !strings.Contains(content, "was: ubuntu-latest") {
		t.Errorf("expected comment with original label, got:\n%s", content)
	}

	foundRunsOn := false
	for _, c := range result.Changes {
		if c.Type == ChangeRunsOn && c.JobName == "build" {
			foundRunsOn = true
		}
	}
	if !foundRunsOn {
		t.Errorf("expected ChangeRunsOn for job 'build', changes: %v", result.Changes)
	}
}

func TestTransformWorkflow_NonstandardLabel(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: blacksmith-4vcpu-ubuntu-2204
    steps:
      - uses: actions/checkout@v4
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs: []migrate.JobInfo{
			{Name: "build", RunsOn: "blacksmith-4vcpu-ubuntu-2204"},
		},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, "runs-on: depot-ubuntu-latest") {
		t.Errorf("expected depot-ubuntu-latest, got:\n%s", content)
	}
	if !strings.Contains(content, "was: blacksmith-4vcpu-ubuntu-2204") {
		t.Errorf("expected comment with original nonstandard label, got:\n%s", content)
	}
	if !strings.Contains(content, "Nonstandard GitHub runner label") {
		t.Errorf("expected nonstandard label reason in comment, got:\n%s", content)
	}
}

func TestTransformWorkflow_ExpressionLabel(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs: []migrate.JobInfo{
			{Name: "build", RunsOn: "${{ matrix.os }}"},
		},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Changes) != 0 {
		t.Errorf("expected no changes for expression label, got %d", len(result.Changes))
	}
}

func TestTransformWorkflow_UnsupportedTrigger_Scalar(t *testing.T) {
	raw := []byte(`name: Release
on: release
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/release.yml",
		Name:     "Release",
		Triggers: []string{"release"},
		Jobs: []migrate.JobInfo{
			{Name: "build", RunsOn: "depot-ubuntu-latest"},
		},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, "Removed unsupported trigger") {
		t.Errorf("expected removed trigger comment, got:\n%s", content)
	}

	foundTrigger := false
	for _, c := range result.Changes {
		if c.Type == ChangeTriggerRemoved {
			foundTrigger = true
		}
	}
	if !foundTrigger {
		t.Errorf("expected ChangeTriggerRemoved, changes: %v", result.Changes)
	}
}

func TestTransformWorkflow_UnsupportedTrigger_Sequence(t *testing.T) {
	raw := []byte(`name: CI
on: [push, release]
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push", "release"},
		Jobs: []migrate.JobInfo{
			{Name: "build", RunsOn: "depot-ubuntu-latest"},
		},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, "Removed unsupported trigger: release") {
		t.Errorf("expected removed trigger comment for release, got:\n%s", content)
	}
	// push should still be present
	if !strings.Contains(content, "push") {
		t.Errorf("expected push trigger to be preserved, got:\n%s", content)
	}
}

func TestTransformWorkflow_UnsupportedTrigger_Mapping(t *testing.T) {
	raw := []byte(`name: CI
on:
  push:
    branches: [main]
  release:
    types: [published]
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push", "release"},
		Jobs: []migrate.JobInfo{
			{Name: "build", RunsOn: "depot-ubuntu-latest"},
		},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, "Removed unsupported trigger: release") {
		t.Errorf("expected removed trigger comment, got:\n%s", content)
	}
	if !strings.Contains(content, "push") {
		t.Errorf("expected push trigger preserved, got:\n%s", content)
	}
	// release trigger and its config should be removed from the on: block
	if strings.Contains(content, "types:") {
		t.Errorf("expected release types config removed, got:\n%s", content)
	}
}

func TestTransformWorkflow_DisabledJob(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
  deploy:
    runs-on: [self-hosted, linux]
    strategy:
      matrix:
        env: [staging, prod]
    steps:
      - uses: actions/checkout@v4
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs: []migrate.JobInfo{
			{Name: "build", RunsOn: "depot-ubuntu-latest"},
			{Name: "deploy", RunsOn: "self-hosted,linux", HasMatrix: true},
		},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, "# DISABLED:") {
		t.Errorf("expected DISABLED comment for deploy job, got:\n%s", content)
	}
	if !result.HasCritical {
		t.Errorf("expected HasCritical to be true")
	}

	// build job should still be functional
	if !strings.Contains(content, "runs-on: depot-ubuntu-latest") {
		t.Errorf("expected build job runs-on preserved, got:\n%s", content)
	}
}

func TestTransformWorkflow_CombinedChanges(t *testing.T) {
	raw := []byte(`name: CI
on:
  push:
    branches: [main]
  release:
    types: [published]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
  test:
    runs-on: blacksmith-4vcpu-ubuntu-2204
    steps:
      - run: echo test
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push", "release"},
		Jobs: []migrate.JobInfo{
			{Name: "build", RunsOn: "ubuntu-latest"},
			{Name: "test", RunsOn: "blacksmith-4vcpu-ubuntu-2204"},
		},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)

	// Header should list all changes
	if !strings.Contains(content, "Changes made:") {
		t.Errorf("expected 'Changes made:' header, got:\n%s", content)
	}

	// Trigger removed
	if !strings.Contains(content, "Removed unsupported trigger") {
		t.Errorf("expected trigger removal note, got:\n%s", content)
	}

	// Both jobs should have updated runs-on
	if !strings.Contains(content, "depot-ubuntu-latest") {
		t.Errorf("expected depot-ubuntu-latest in output, got:\n%s", content)
	}

	// Should have at least 3 changes (1 trigger + 2 runs-on)
	if len(result.Changes) < 3 {
		t.Errorf("expected at least 3 changes, got %d: %v", len(result.Changes), result.Changes)
	}
}

func TestTransformWorkflow_RunsOnSequence(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on:
      - ubuntu-latest
      - self-hosted
    steps:
      - uses: actions/checkout@v4
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs: []migrate.JobInfo{
			{Name: "build", RunsOn: "ubuntu-latest,self-hosted"},
		},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	// Both labels should be mapped
	if !strings.Contains(content, "depot-ubuntu-latest") {
		t.Errorf("expected depot-ubuntu-latest, got:\n%s", content)
	}
}

func TestTransformWorkflow_CondensedHeader(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  lint:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
  build:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
  test:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs: []migrate.JobInfo{
			{Name: "lint", RunsOn: "ubuntu-24.04"},
			{Name: "build", RunsOn: "ubuntu-24.04"},
			{Name: "test", RunsOn: "ubuntu-24.04"},
		},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	// Should condense to a single "throughout" line
	if !strings.Contains(content, "throughout") {
		t.Errorf("expected condensed 'throughout' in header, got:\n%s", content)
	}
	// Should NOT list individual jobs
	if strings.Contains(content, "in job") {
		t.Errorf("expected no per-job lines in header, got:\n%s", content)
	}
}

func TestBuildHeaderComment_NoChanges(t *testing.T) {
	wf := &migrate.WorkflowFile{Path: ".github/workflows/ci.yml"}
	header := buildHeaderComment(wf, nil)
	if !strings.Contains(header, "No changes were necessary") {
		t.Errorf("expected no-changes header, got: %s", header)
	}
}

func TestBuildHeaderComment_WithChanges(t *testing.T) {
	wf := &migrate.WorkflowFile{Path: ".github/workflows/ci.yml"}
	changes := []ChangeRecord{
		{Type: ChangeRunsOn, JobName: "build", Detail: "Changed runs-on from \"ubuntu-latest\" to \"depot-ubuntu-latest\" in job \"build\""},
	}
	header := buildHeaderComment(wf, changes)
	if !strings.Contains(header, "Changes made:") {
		t.Errorf("expected 'Changes made:' in header, got: %s", header)
	}
	if !strings.Contains(header, "ubuntu-latest") {
		t.Errorf("expected change detail in header, got: %s", header)
	}
}
