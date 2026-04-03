package transform

import (
	"bytes"
	"os"
	"path/filepath"
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

	result, err := TransformWorkflow(raw, wf, report, nil)
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

	result, err := TransformWorkflow(raw, wf, report, nil)
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

	result, err := TransformWorkflow(raw, wf, report, nil)
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

	result, err := TransformWorkflow(raw, wf, report, nil)
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

	result, err := TransformWorkflow(raw, wf, report, nil)
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

	result, err := TransformWorkflow(raw, wf, report, nil)
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

	result, err := TransformWorkflow(raw, wf, report, nil)
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

	result, err := TransformWorkflow(raw, wf, report, nil)
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

	result, err := TransformWorkflow(raw, wf, report, nil)
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

	result, err := TransformWorkflow(raw, wf, report, nil)
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

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	// Should condense to a single generalized line
	if !strings.Contains(content, "Changed GitHub runs-on labels to their Depot equivalents") {
		t.Errorf("expected generalized runs-on summary in header, got:\n%s", content)
	}
	// Should NOT list individual jobs
	if strings.Contains(content, "in job") {
		t.Errorf("expected no per-job lines in header, got:\n%s", content)
	}
}

func TestTransformWorkflow_RewritesLocalActionPaths(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: ./.github/actions/setup-node
      - uses: ./.github/actions/deploy
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

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if strings.Contains(content, "./.github/actions/") {
		t.Errorf("expected ./.github/actions/ to be rewritten, got:\n%s", content)
	}
	if !strings.Contains(content, "./.depot/actions/setup-node") {
		t.Errorf("expected ./.depot/actions/setup-node, got:\n%s", content)
	}
	if !strings.Contains(content, "./.depot/actions/deploy") {
		t.Errorf("expected ./.depot/actions/deploy, got:\n%s", content)
	}
	// Remote actions should be untouched
	if !strings.Contains(content, "actions/checkout@v4") {
		t.Errorf("expected remote action unchanged, got:\n%s", content)
	}

	pathRewriteCount := 0
	for _, c := range result.Changes {
		if c.Type == ChangePathRewritten {
			pathRewriteCount++
		}
	}
	if pathRewriteCount != 1 {
		t.Errorf("expected exactly 1 ChangePathRewritten (deduplicated), got %d", pathRewriteCount)
	}
}

func TestTransformWorkflow_RewritesBareGitHubPaths(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: .github/actions/my-action
      - uses: .github/workflows/reusable.yml
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

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, ".depot/actions/my-action") {
		t.Errorf("expected .depot/actions/my-action, got:\n%s", content)
	}
	if !strings.Contains(content, ".depot/workflows/reusable.yml") {
		t.Errorf("expected .depot/workflows/reusable.yml, got:\n%s", content)
	}
}

func TestTransformWorkflow_PreservesNonMigratedGitHubPaths(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: cat .github/dependabot.yml
      - run: cp .github/config.json dist/
      - run: ls .github/ISSUE_TEMPLATE/
      - uses: ./.github/actions/setup
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

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	// Non-migrated paths should be preserved
	if !strings.Contains(content, ".github/dependabot.yml") {
		t.Errorf("expected .github/dependabot.yml preserved, got:\n%s", content)
	}
	if !strings.Contains(content, ".github/config.json") {
		t.Errorf("expected .github/config.json preserved, got:\n%s", content)
	}
	if !strings.Contains(content, ".github/ISSUE_TEMPLATE/") {
		t.Errorf("expected .github/ISSUE_TEMPLATE/ preserved, got:\n%s", content)
	}
	// Migrated path should be rewritten
	if !strings.Contains(content, "./.depot/actions/setup") {
		t.Errorf("expected ./.depot/actions/setup, got:\n%s", content)
	}
}

func TestTransformWorkflow_SkipsRemoteRepoGitHubPaths(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: org/repo/.github/workflows/reusable.yml@main
      - uses: ./.github/actions/local-action
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

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	// Remote reference should be untouched
	if !strings.Contains(content, "org/repo/.github/workflows/reusable.yml@main") {
		t.Errorf("expected remote repo reference unchanged, got:\n%s", content)
	}
	// Local reference should be rewritten
	if !strings.Contains(content, "./.depot/actions/local-action") {
		t.Errorf("expected local action rewritten, got:\n%s", content)
	}
}

func TestTransformWorkflow_PathRewriteInHeader(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: ./.github/actions/setup
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

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, "Rewrote .github/ path references to .depot/") {
		t.Errorf("expected path rewrite note in header, got:\n%s", content)
	}
	// Source line in header should still reference original .github/ path
	if !strings.Contains(content, "# Source: .github/workflows/ci.yml") {
		t.Errorf("expected original source path in header, got:\n%s", content)
	}
}

func TestTransformWorkflow_ExpressionExpandedPaths(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: ${{ github.workspace }}/.github/actions/setup/run.sh
      - run: $GITHUB_WORKSPACE/.github/actions/build/compile.sh
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

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, "}}/.depot/actions/setup/run.sh") {
		t.Errorf("expected expression-expanded path rewritten, got:\n%s", content)
	}
	if !strings.Contains(content, "WORKSPACE/.depot/actions/build/compile.sh") {
		t.Errorf("expected env var path rewritten, got:\n%s", content)
	}
}

func TestTransformWorkflow_PartialMigration(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: ./.github/workflows/reusable-build.yml
      - uses: ./.github/workflows/reusable-deploy.yml
      - uses: ./.github/actions/setup
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

	// Only reusable-build.yml was migrated, not reusable-deploy.yml
	migratedWorkflows := map[string]bool{
		"ci.yml":             true,
		"reusable-build.yml": true,
	}

	result, err := TransformWorkflow(raw, wf, report, migratedWorkflows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	// Migrated workflow should be rewritten
	if !strings.Contains(content, "./.depot/workflows/reusable-build.yml") {
		t.Errorf("expected migrated workflow rewritten, got:\n%s", content)
	}
	// Non-migrated workflow should be preserved
	if !strings.Contains(content, "./.github/workflows/reusable-deploy.yml") {
		t.Errorf("expected non-migrated workflow preserved, got:\n%s", content)
	}
	// Actions are always rewritten regardless of workflow filtering
	if !strings.Contains(content, "./.depot/actions/setup") {
		t.Errorf("expected action always rewritten, got:\n%s", content)
	}
}

func TestRewriteGitHubPaths(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		changed bool
	}{
		{
			name:    "explicit local action path",
			input:   "./.github/actions/setup-node",
			want:    "./.depot/actions/setup-node",
			changed: true,
		},
		{
			name:    "explicit local workflow path",
			input:   "./.github/workflows/reusable.yml",
			want:    "./.depot/workflows/reusable.yml",
			changed: true,
		},
		{
			name:    "bare local action path",
			input:   ".github/actions/setup",
			want:    ".depot/actions/setup",
			changed: true,
		},
		{
			name:    "bare local workflow path",
			input:   ".github/workflows/helper.yml",
			want:    ".depot/workflows/helper.yml",
			changed: true,
		},
		{
			name:    "non-migrated github file preserved",
			input:   "cat .github/dependabot.yml",
			want:    "cat .github/dependabot.yml",
			changed: false,
		},
		{
			name:    "non-migrated github directory preserved",
			input:   "ls .github/ISSUE_TEMPLATE/",
			want:    "ls .github/ISSUE_TEMPLATE/",
			changed: false,
		},
		{
			name:    "non-migrated config preserved",
			input:   "cp .github/config.json dist/",
			want:    "cp .github/config.json dist/",
			changed: false,
		},
		{
			name:    "remote repo reference preserved",
			input:   "org/repo/.github/workflows/reusable.yml@main",
			want:    "org/repo/.github/workflows/reusable.yml@main",
			changed: false,
		},
		{
			name:    "remote repo action preserved",
			input:   "org/repo/.github/actions/shared@v2",
			want:    "org/repo/.github/actions/shared@v2",
			changed: false,
		},
		{
			name:    "no github reference",
			input:   "actions/checkout@v4",
			want:    "actions/checkout@v4",
			changed: false,
		},
		{
			name:    "multiple local references",
			input:   "./.github/actions/a and ./.github/actions/b",
			want:    "./.depot/actions/a and ./.depot/actions/b",
			changed: true,
		},
		{
			name:    "mixed local and remote",
			input:   "./.github/actions/local org/repo/.github/workflows/remote.yml@v1",
			want:    "./.depot/actions/local org/repo/.github/workflows/remote.yml@v1",
			changed: true,
		},
		{
			name:    "mixed migrated and non-migrated",
			input:   "./.github/actions/setup && cat .github/config.json",
			want:    "./.depot/actions/setup && cat .github/config.json",
			changed: true,
		},
		{
			name:    "bare actions directory no trailing slash",
			input:   "ls .github/actions",
			want:    "ls .depot/actions",
			changed: true,
		},
		{
			name:    "bare workflows directory no trailing slash",
			input:   "ls .github/workflows",
			want:    "ls .depot/workflows",
			changed: true,
		},
		{
			name:    "explicit bare directory no trailing slash",
			input:   "ls ./.github/actions",
			want:    "ls ./.depot/actions",
			changed: true,
		},
		{
			name:    "similar directory name not rewritten",
			input:   ".github/actions-custom/setup.sh",
			want:    ".github/actions-custom/setup.sh",
			changed: false,
		},
		{
			name:    "similar directory name with dot prefix not rewritten",
			input:   "./.github/actions-custom/setup.sh",
			want:    "./.github/actions-custom/setup.sh",
			changed: false,
		},
		{
			name:    "expression-expanded path rewritten",
			input:   "${{ github.workspace }}/.github/actions/setup/run.sh",
			want:    "${{ github.workspace }}/.depot/actions/setup/run.sh",
			changed: true,
		},
		{
			name:    "env var path rewritten",
			input:   "$GITHUB_WORKSPACE/.github/actions/build/compile.sh",
			want:    "$GITHUB_WORKSPACE/.depot/actions/build/compile.sh",
			changed: true,
		},
		{
			name:    "absolute path rewritten",
			input:   "/home/runner/work/repo/repo/.github/workflows/ci.yml",
			want:    "/home/runner/work/repo/repo/.depot/workflows/ci.yml",
			changed: true,
		},
		{
			name:    "part of longer name not rewritten",
			input:   "myapp.github/actions/setup",
			want:    "myapp.github/actions/setup",
			changed: false,
		},
		{
			name:    "CODEOWNERS preserved",
			input:   "cat .github/CODEOWNERS",
			want:    "cat .github/CODEOWNERS",
			changed: false,
		},
		{
			name:    "FUNDING.yml preserved",
			input:   ".github/FUNDING.yml",
			want:    ".github/FUNDING.yml",
			changed: false,
		},
		{
			name:    "github URL preserved",
			input:   "https://github.com/org/repo/tree/main/.github/actions/setup",
			want:    "https://github.com/org/repo/tree/main/.github/actions/setup",
			changed: false,
		},
		{
			name:    "github URL with workflows preserved",
			input:   "see https://github.com/org/repo/blob/main/.github/workflows/ci.yml for details",
			want:    "see https://github.com/org/repo/blob/main/.github/workflows/ci.yml for details",
			changed: false,
		},
		{
			name:    "URL before shell delimiter does not block rewrite",
			input:   "curl https://api.com/status;.github/actions/setup/run.sh",
			want:    "curl https://api.com/status;.depot/actions/setup/run.sh",
			changed: true,
		},
		{
			name:    "subshell boundary rewritten",
			input:   "$(.github/actions/setup/version.sh)",
			want:    "$(.depot/actions/setup/version.sh)",
			changed: true,
		},
		{
			name:    "assignment boundary rewritten",
			input:   "CONFIG=.github/actions/setup/config.json",
			want:    "CONFIG=.depot/actions/setup/config.json",
			changed: true,
		},
		{
			name:    "pipe boundary rewritten",
			input:   "cat file|.github/actions/setup/run.sh",
			want:    "cat file|.depot/actions/setup/run.sh",
			changed: true,
		},
		{
			name:    "semicolon boundary rewritten",
			input:   "cd repo;.github/actions/setup/run.sh",
			want:    "cd repo;.depot/actions/setup/run.sh",
			changed: true,
		},
		{
			name:    "parenthesized remote ref preserved",
			input:   "(org/repo/.github/actions/setup)",
			want:    "(org/repo/.github/actions/setup)",
			changed: false,
		},
		{
			name:    "parenthesized local ref rewritten",
			input:   "(.github/actions/setup/run.sh)",
			want:    "(.depot/actions/setup/run.sh)",
			changed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := rewriteGitHubPaths(tt.input, nil)
			if got != tt.want {
				t.Errorf("rewriteGitHubPaths(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if changed != tt.changed {
				t.Errorf("rewriteGitHubPaths(%q) changed = %v, want %v", tt.input, changed, tt.changed)
			}
		})
	}
}

func TestRewriteGitHubPaths_WorkflowFiltering(t *testing.T) {
	migrated := map[string]bool{"ci.yml": true, "build.yml": true}

	tests := []struct {
		name    string
		input   string
		want    string
		changed bool
	}{
		{
			name:    "migrated workflow rewritten",
			input:   "./.github/workflows/ci.yml",
			want:    "./.depot/workflows/ci.yml",
			changed: true,
		},
		{
			name:    "non-migrated workflow preserved",
			input:   "./.github/workflows/deploy.yml",
			want:    "./.github/workflows/deploy.yml",
			changed: false,
		},
		{
			name:    "bare workflows dir preserved when filtering",
			input:   "ls .github/workflows",
			want:    "ls .github/workflows",
			changed: false,
		},
		{
			name:    "actions always rewritten regardless",
			input:   "./.github/actions/setup",
			want:    "./.depot/actions/setup",
			changed: true,
		},
		{
			name:    "semicolon-terminated migrated workflow rewritten",
			input:   "source .github/workflows/ci.yml;echo done",
			want:    "source .depot/workflows/ci.yml;echo done",
			changed: true,
		},
		{
			name:    "pipe-terminated migrated workflow rewritten",
			input:   "cat .github/workflows/ci.yml|grep foo",
			want:    "cat .depot/workflows/ci.yml|grep foo",
			changed: true,
		},
		{
			name:    "ampersand-terminated migrated workflow rewritten",
			input:   "cat .github/workflows/build.yml&&echo ok",
			want:    "cat .depot/workflows/build.yml&&echo ok",
			changed: true,
		},
		{
			name:    "semicolon-terminated non-migrated workflow preserved",
			input:   "cat .github/workflows/deploy.yml;echo done",
			want:    "cat .github/workflows/deploy.yml;echo done",
			changed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := rewriteGitHubPaths(tt.input, migrated)
			if got != tt.want {
				t.Errorf("rewriteGitHubPaths(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if changed != tt.changed {
				t.Errorf("rewriteGitHubPaths(%q) changed = %v, want %v", tt.input, changed, tt.changed)
			}
		})
	}
}

func TestRewriteGitHubPathsInDir(t *testing.T) {
	dir := t.TempDir()

	// Composite action with local .github/ reference
	actionDir := filepath.Join(dir, "setup")
	if err := os.MkdirAll(actionDir, 0755); err != nil {
		t.Fatal(err)
	}
	actionYAML := `name: Setup
description: Setup the project
runs:
  using: composite
  steps:
    - uses: ./.github/actions/install-deps
    - run: echo "done"
`
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Shell script with execute bit referencing migrated and non-migrated .github/ paths
	script := `#!/bin/bash
cp .github/actions/shared/config.sh .
cat .github/dependabot.yml
`
	scriptPath := filepath.Join(actionDir, "setup.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	// Binary file with .github/actions bytes — should be skipped
	binaryContent := []byte("some\x00binary\x00.github/actions/setup data")
	if err := os.WriteFile(filepath.Join(actionDir, "tool.wasm"), binaryContent, 0644); err != nil {
		t.Fatal(err)
	}

	// File with no .github/ references (should be untouched)
	clean := `name: Clean Action
runs:
  using: node20
  main: index.js
`
	cleanDir := filepath.Join(dir, "clean")
	if err := os.MkdirAll(cleanDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cleanDir, "action.yml"), []byte(clean), 0644); err != nil {
		t.Fatal(err)
	}

	rewritten, err := RewriteGitHubPathsInDir(dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rewritten != 2 {
		t.Errorf("expected 2 files rewritten, got %d", rewritten)
	}

	// Verify action.yml was rewritten
	content, _ := os.ReadFile(filepath.Join(actionDir, "action.yml"))
	if strings.Contains(string(content), "./.github/actions/") {
		t.Errorf("expected .github/actions reference rewritten in action.yml, got:\n%s", content)
	}
	if !strings.Contains(string(content), "./.depot/actions/install-deps") {
		t.Errorf("expected .depot/actions reference in action.yml, got:\n%s", content)
	}

	// Verify shell script: migrated path rewritten, non-migrated path preserved
	scriptContent, _ := os.ReadFile(scriptPath)
	if !strings.Contains(string(scriptContent), ".depot/actions/shared/config.sh") {
		t.Errorf("expected .github/actions/ rewritten in setup.sh, got:\n%s", scriptContent)
	}
	if !strings.Contains(string(scriptContent), ".github/dependabot.yml") {
		t.Errorf("expected .github/dependabot.yml preserved in setup.sh, got:\n%s", scriptContent)
	}

	// Verify script preserved its execute permission
	scriptInfo, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if scriptInfo.Mode().Perm()&0111 == 0 {
		t.Errorf("expected setup.sh to retain execute permission, got %v", scriptInfo.Mode().Perm())
	}

	// Verify binary file was not modified
	binaryResult, _ := os.ReadFile(filepath.Join(actionDir, "tool.wasm"))
	if !bytes.Equal(binaryResult, binaryContent) {
		t.Errorf("expected binary file to be untouched")
	}

	// Verify clean file was not touched
	cleanContent, _ := os.ReadFile(filepath.Join(cleanDir, "action.yml"))
	if string(cleanContent) != clean {
		t.Errorf("expected clean file unchanged, got:\n%s", cleanContent)
	}
}

func TestBuildHeaderComment_NoChanges(t *testing.T) {
	wf := &migrate.WorkflowFile{Path: ".github/workflows/ci.yml"}
	header := buildHeaderComment(wf, nil)
	if !strings.Contains(header, "No changes were necessary") {
		t.Errorf("expected no-changes header, got: %s", header)
	}
}

func TestBuildHeaderComment_WithStandardChanges(t *testing.T) {
	wf := &migrate.WorkflowFile{Path: ".github/workflows/ci.yml"}
	changes := []ChangeRecord{
		{Type: ChangeRunsOn, JobName: "build", Detail: "Changed runs-on from \"ubuntu-latest\" to \"depot-ubuntu-latest\" in job \"build\""},
	}
	header := buildHeaderComment(wf, changes)
	if !strings.Contains(header, "Changes made:") {
		t.Errorf("expected 'Changes made:' in header, got: %s", header)
	}
	if !strings.Contains(header, "Changed GitHub runs-on labels to their Depot equivalents") {
		t.Errorf("expected generalized summary in header, got: %s", header)
	}
}

func TestBuildHeaderComment_WithNonstandardChanges(t *testing.T) {
	wf := &migrate.WorkflowFile{Path: ".github/workflows/ci.yml"}
	changes := []ChangeRecord{
		{Type: ChangeRunsOn, JobName: "build", Detail: "Changed runs-on from \"blacksmith-4vcpu\" to \"depot-ubuntu-latest\" in job \"build\""},
	}
	header := buildHeaderComment(wf, changes)
	if !strings.Contains(header, "blacksmith-4vcpu") {
		t.Errorf("expected nonstandard label detail in header, got: %s", header)
	}
}
