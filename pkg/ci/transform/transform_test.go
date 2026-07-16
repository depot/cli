package transform

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/depot/cli/pkg/ci/compat"
	"github.com/depot/cli/pkg/ci/migrate"
	"gopkg.in/yaml.v3"
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

func TestTransformWorkflow_RewritesYAMLComments(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      # Run the setup from .github/actions/setup
      - uses: ./.github/actions/setup # local action at .github/actions/setup
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
	// Head comment should be rewritten
	if strings.Contains(content, "# Run the setup from .github/actions/setup") {
		t.Errorf("expected head comment .github/ path rewritten, got:\n%s", content)
	}
	if !strings.Contains(content, ".depot/actions/setup") {
		t.Errorf("expected .depot/actions/setup in comments, got:\n%s", content)
	}
	// Line comment should be rewritten
	if strings.Contains(content, "# local action at .github/actions/setup") {
		t.Errorf("expected line comment .github/ path rewritten, got:\n%s", content)
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

func TestTransformWorkflow_PartialMigration_SiblingScriptPath(t *testing.T) {
	// A migrated workflow references a sibling helper script that lives under
	// .github/workflows/. Under a partial migration the rewrite is gated by the
	// migratedWorkflows set, keyed by path relative to the workflows dir — so a nested
	// script key like "scripts/build.sh" must gate its reference the same way a
	// top-level workflow filename does. This is the membership check the command layer
	// relies on after it adds copied siblings to the set.
	raw := []byte(`name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - run: bash .github/workflows/scripts/build.sh
      - run: bash .github/workflows/scripts/other.sh
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}
	report := compat.AnalyzeWorkflow(wf)

	// build.sh was copied (so it joins the set); other.sh was not.
	migratedWorkflows := map[string]bool{
		"ci.yml":           true,
		"scripts/build.sh": true,
	}

	result, err := TransformWorkflow(raw, wf, report, migratedWorkflows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, ".depot/workflows/scripts/build.sh") {
		t.Errorf("expected copied sibling reference rewritten to .depot/, got:\n%s", content)
	}
	if !strings.Contains(content, ".github/workflows/scripts/other.sh") {
		t.Errorf("expected un-copied sibling reference left at .github/, got:\n%s", content)
	}
}

func TestRewriteGitHubPaths_PartialMigration_EscapedSiblingTail(t *testing.T) {
	// An escaped nested-sibling reference embedded in a code string keeps escaped
	// separators in its tail (scripts\/build.sh). The allow-list is keyed by plain
	// slash paths, so the tail must be unescaped before the lookup — otherwise the
	// copied sibling reference would be left pointing at .github/.
	set := map[string]bool{"scripts/build.sh": true}
	input := `node -e "require('fs').existsSync('.github\/workflows\/scripts\/build.sh')"`

	got, changed := rewriteGitHubPaths(input, set)
	if !changed {
		t.Fatalf("expected escaped sibling reference rewritten, got unchanged: %q", got)
	}
	if !strings.Contains(got, `.depot\/workflows\/scripts\/build.sh`) {
		t.Errorf("expected escaped tail rewritten to .depot/, got:\n%s", got)
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
		{
			name:    "regex escaped dot rewritten",
			input:   `const re = /\.github\/actions\//`,
			want:    `const re = /\.depot\/actions\//`,
			changed: true,
		},
		{
			name:    "regex escaped dot literal slash rewritten",
			input:   `\.github/actions/setup`,
			want:    `\.depot/actions/setup`,
			changed: true,
		},
		{
			name:    "escaped dot inside longer token not rewritten",
			input:   `myapp\.github/actions/setup`,
			want:    `myapp\.github/actions/setup`,
			changed: false,
		},
		{
			name:    "python raw regex workflows rewritten",
			input:   `re.compile(r"\.github\/workflows")`,
			want:    `re.compile(r"\.depot\/workflows")`,
			changed: true,
		},
		{
			name:    "escaped-slash url left untouched",
			input:   `"https:\/\/github.com\/org\/repo\/tree\/main\/.github\/actions"`,
			want:    `"https:\/\/github.com\/org\/repo\/tree\/main\/.github\/actions"`,
			changed: false,
		},
		{
			name:    "escaped-slash remote ref left untouched",
			input:   `uses: org\/repo\/.github\/workflows\/reusable.yml`,
			want:    `uses: org\/repo\/.github\/workflows\/reusable.yml`,
			changed: false,
		},
		{
			name:    "fully escaped remote ref left untouched",
			input:   `ref: org\/repo\/\.github\/actions\/setup`,
			want:    `ref: org\/repo\/\.github\/actions\/setup`,
			changed: false,
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

	// Verify symlinks are skipped: create a symlink and confirm it's not followed
	symlinkTarget := filepath.Join(actionDir, "action.yml")
	symlinkPath := filepath.Join(cleanDir, "link.yml")
	if err := os.Symlink(symlinkTarget, symlinkPath); err != nil {
		t.Fatal(err)
	}
	// Re-run after adding symlink — count should stay at 0 since action.yml is already rewritten
	rewritten2, err := RewriteGitHubPathsInDir(dir, nil)
	if err != nil {
		t.Fatalf("unexpected error on second pass: %v", err)
	}
	if rewritten2 != 0 {
		t.Errorf("expected 0 files rewritten on second pass (symlink should be skipped), got %d", rewritten2)
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

func TestSanitizeBlockScalars_PreservesBackslashContinuation(t *testing.T) {
	// A line ending in a backslash followed by whitespace is a literal escaped
	// space in shell, not a line continuation. Trimming it down to a trailing
	// backslash would change what the command does, so it must be left intact —
	// while an ordinary trailing-whitespace line is still trimmed.
	n := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Style: yaml.LiteralStyle,
		Value: "echo foo \\   \necho bar   \n",
	}
	trimTrailingWhitespace(n)

	lines := strings.Split(n.Value, "\n")
	if lines[0] != "echo foo \\   " {
		t.Errorf("expected backslash-continuation line preserved, got %q", lines[0])
	}
	if lines[1] != "echo bar" {
		t.Errorf("expected ordinary trailing whitespace trimmed, got %q", lines[1])
	}
}

func TestSanitizeBlockScalars_PreservesHeredoc(t *testing.T) {
	// A heredoc payload may depend on trailing whitespace, so a scalar containing
	// a heredoc operator is left entirely untouched rather than tidied.
	original := "cat <<EOF\npadded line   \nEOF\n"
	n := &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.LiteralStyle, Value: original}
	trimTrailingWhitespace(n)
	if n.Value != original {
		t.Errorf("expected heredoc scalar left untouched, got %q", n.Value)
	}
}

func TestSanitizeBlockScalars_PreservesQuotedLineSpan(t *testing.T) {
	// A single-quoted shell string can span lines; the trailing spaces after "foo"
	// are string data continuing to the next line, so trimming them would change the
	// value the shell sees. The line ending mid-quote must be left intact, while an
	// ordinary trailing-whitespace line after the quote closes is still trimmed.
	n := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Style: yaml.LiteralStyle,
		Value: "printf '%s' 'foo  \nbar'\necho done   \n",
	}
	trimTrailingWhitespace(n)

	lines := strings.Split(n.Value, "\n")
	if lines[0] != "printf '%s' 'foo  " {
		t.Errorf("expected line inside open quote preserved, got %q", lines[0])
	}
	if lines[2] != "echo done" {
		t.Errorf("expected trailing whitespace outside quotes trimmed, got %q", lines[2])
	}
}

func TestSanitizeBlockScalars_PreservesPowerShellContinuation(t *testing.T) {
	// PowerShell uses a backtick and cmd uses a caret as line-continuation escapes
	// rather than a backslash. A trailing "escape + spaces" is a literal escaped space,
	// not a continuation, so trimming it would splice the line onto the next and change
	// what runs. Both must be left intact, while an ordinary line is still trimmed.
	n := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Style: yaml.LiteralStyle,
		Value: "Write-Host foo `   \ncopy a b ^  \necho ok   \n",
	}
	trimTrailingWhitespace(n)

	lines := strings.Split(n.Value, "\n")
	if lines[0] != "Write-Host foo `   " {
		t.Errorf("expected backtick-continuation line preserved, got %q", lines[0])
	}
	if lines[1] != "copy a b ^  " {
		t.Errorf("expected caret-continuation line preserved, got %q", lines[1])
	}
	if lines[2] != "echo ok" {
		t.Errorf("expected ordinary trailing whitespace trimmed, got %q", lines[2])
	}
}

func TestTransformWorkflow_PreservesNonRunBlockScalar(t *testing.T) {
	// A non-run block scalar (an action input) may carry meaningful trailing
	// whitespace — e.g. Markdown hard line breaks — so it must survive migration
	// byte-for-byte, even though that forces yaml.v3's quoted fallback.
	raw := []byte("name: CI\non: push\njobs:\n  build:\n    runs-on: depot-ubuntu-latest\n    steps:\n      - uses: some/action@v1\n        with:\n          body: |\n            first line  \n            second line\n")

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Re-parse the emitted YAML and confirm the body payload kept its trailing
	// spaces exactly.
	var parsed struct {
		Jobs map[string]struct {
			Steps []struct {
				With struct {
					Body string `yaml:"body"`
				} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(result.Content, &parsed); err != nil {
		t.Fatalf("failed to re-parse migrated YAML: %v", err)
	}
	body := parsed.Jobs["build"].Steps[0].With.Body
	if body != "first line  \nsecond line\n" {
		t.Errorf("expected non-run body preserved byte-for-byte, got %q", body)
	}
}

func TestTransformWorkflow_PreservesRunBlockScalar(t *testing.T) {
	// The run block has a line with trailing whitespace, which previously forced
	// yaml.v3 to flatten the whole block into a double-quoted "\n"-escaped string.
	raw := []byte("name: CI\non: push\njobs:\n  build:\n    runs-on: depot-ubuntu-latest\n    steps:\n      - run: |\n          echo hello   \n          make build\n          ls .github/actions\n")

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, "run: |") {
		t.Errorf("expected literal block style 'run: |' to be preserved, got:\n%s", content)
	}
	if strings.Contains(content, `\n`) {
		t.Errorf("expected no escaped newlines in flattened run block, got:\n%s", content)
	}
	// The path rewrite inside the block should still have happened.
	if !strings.Contains(content, ".depot/actions") {
		t.Errorf("expected .github/actions rewritten inside run block, got:\n%s", content)
	}
}

func TestTransformWorkflow_PreservesWithRunInput(t *testing.T) {
	// A block scalar passed to an action input named "run" (under with:) is not a shell
	// step, so its trailing whitespace must survive migration byte-for-byte even though
	// that forces yaml.v3's quoted fallback.
	raw := []byte("name: CI\non: push\njobs:\n  build:\n    runs-on: depot-ubuntu-latest\n    steps:\n      - uses: some/action@v1\n        with:\n          run: |\n            first line  \n            second line\n")

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed struct {
		Jobs map[string]struct {
			Steps []struct {
				With struct {
					Run string `yaml:"run"`
				} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(result.Content, &parsed); err != nil {
		t.Fatalf("failed to re-parse migrated YAML: %v", err)
	}
	run := parsed.Jobs["build"].Steps[0].With.Run
	if run != "first line  \nsecond line\n" {
		t.Errorf("expected with.run input preserved byte-for-byte, got %q", run)
	}
}

func TestTransformWorkflow_PreservesCmdRunBlock(t *testing.T) {
	// Under shell: cmd, trailing spaces can be significant (e.g. `set NAME=value   `
	// stores them), so a cmd run block must be left byte-exact rather than trimmed.
	raw := []byte("name: CI\non: push\njobs:\n  build:\n    runs-on: depot-ubuntu-latest\n    steps:\n      - shell: cmd\n        run: |\n          set NAME=value   \n          echo %NAME%\n")

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(result.Content, &parsed); err != nil {
		t.Fatalf("failed to re-parse migrated YAML: %v", err)
	}
	run := parsed.Jobs["build"].Steps[0].Run
	if run != "set NAME=value   \necho %NAME%\n" {
		t.Errorf("expected cmd run block preserved byte-for-byte, got %q", run)
	}
}

func TestTransformWorkflow_SparseCheckoutSuperset_BlockScalar(t *testing.T) {
	raw := []byte("name: CI\non: push\njobs:\n  build:\n    runs-on: depot-ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n        with:\n          sparse-checkout: |\n            .github/workflows\n            .github/actions\n            src\n      - run: bash .github/scripts/deploy.sh\n")

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	// Originals kept so git still materializes .github/ for the deploy script.
	if !strings.Contains(content, ".github/workflows") {
		t.Errorf("expected .github/workflows kept in sparse-checkout, got:\n%s", content)
	}
	if !strings.Contains(content, ".github/actions") {
		t.Errorf("expected .github/actions kept in sparse-checkout, got:\n%s", content)
	}
	// Depot siblings added so migrated content is materialized too.
	if !strings.Contains(content, ".depot/workflows") {
		t.Errorf("expected .depot/workflows added to sparse-checkout, got:\n%s", content)
	}
	if !strings.Contains(content, ".depot/actions") {
		t.Errorf("expected .depot/actions added to sparse-checkout, got:\n%s", content)
	}
	// The unmigrated script reference must stay pointing at .github/.
	if !strings.Contains(content, "bash .github/scripts/deploy.sh") {
		t.Errorf("expected script reference to stay at .github/, got:\n%s", content)
	}

	sparseChanges := 0
	for _, c := range result.Changes {
		if c.Type == ChangeSparseCheckout {
			sparseChanges++
		}
	}
	if sparseChanges != 1 {
		t.Errorf("expected exactly 1 ChangeSparseCheckout, got %d", sparseChanges)
	}
}

func TestTransformWorkflow_SparseCheckoutSuperset_Sequence(t *testing.T) {
	raw := []byte("name: CI\non: push\njobs:\n  build:\n    runs-on: depot-ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n        with:\n          sparse-checkout:\n            - .github/actions\n            - src\n")

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, "- .github/actions") {
		t.Errorf("expected .github/actions kept in sparse-checkout list, got:\n%s", content)
	}
	if !strings.Contains(content, "- .depot/actions") {
		t.Errorf("expected .depot/actions added to sparse-checkout list, got:\n%s", content)
	}
}

func TestTransformWorkflow_SparseCheckoutSuperset_MirrorsNegation(t *testing.T) {
	// In non-cone mode a "!" pattern excludes a path. The migrated superset must mirror
	// the negation, otherwise the added positive parent (.depot/actions) would re-include
	// files the original explicitly excluded (!.github/actions/cache).
	raw := []byte("name: CI\non: push\njobs:\n  build:\n    runs-on: depot-ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n        with:\n          sparse-checkout-cone-mode: false\n          sparse-checkout: |\n            .github/actions\n            !.github/actions/cache\n")

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, "!.depot/actions/cache") {
		t.Errorf("expected negated pattern mirrored to .depot/, got:\n%s", content)
	}
	// The mirrored exclusion must follow the positive .depot/actions include so that
	// gitignore-style ordering still excludes the cache directory.
	incl := strings.Index(content, ".depot/actions")
	excl := strings.Index(content, "!.depot/actions/cache")
	if incl < 0 || excl < 0 || excl < incl {
		t.Errorf("expected .depot/actions include before !.depot/actions/cache exclude, got:\n%s", content)
	}
}

func TestTransformWorkflow_SparseCheckoutSuperset_MirrorsNegationPreservesSourceOrder(t *testing.T) {
	// When the source lists an exclusion before its positive parent include — an ordering
	// under which the original .github/ checkout already re-includes the "excluded" path,
	// since the last matching gitignore-style pattern wins — the .depot/ mirror must
	// reproduce that same order rather than silently "fixing" it. Faithful mirroring keeps
	// the migrated checkout behaving exactly like the source; reordering would make .depot/
	// diverge from .github/. (.github/ and .depot/ patterns never cross-match, so the
	// interleaved .github/ lines don't affect which .depot/ paths materialize.)
	raw := []byte("name: CI\non: push\njobs:\n  build:\n    runs-on: depot-ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n        with:\n          sparse-checkout-cone-mode: false\n          sparse-checkout: |\n            !.github/actions/cache\n            .github/actions\n")

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}
	report := compat.AnalyzeWorkflow(wf)

	result, err := TransformWorkflow(raw, wf, report, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	// Locate the mirrored lines by trimmed equality so the assertion is independent of
	// how yaml.v3 re-indents the block scalar (and so the ".depot/actions" include isn't
	// confused with the "!.depot/actions/cache" substring).
	exclLine, inclLine := -1, -1
	for i, l := range strings.Split(content, "\n") {
		switch strings.TrimSpace(l) {
		case "!.depot/actions/cache":
			exclLine = i
		case ".depot/actions":
			inclLine = i
		}
	}
	if exclLine < 0 {
		t.Fatalf("expected negated pattern mirrored to .depot/, got:\n%s", content)
	}
	if inclLine < 0 {
		t.Fatalf("expected .depot/actions include mirrored, got:\n%s", content)
	}
	// Source had the exclude before the include; the mirror must keep that order.
	if exclLine > inclLine {
		t.Errorf("expected mirrored !.depot/actions/cache to precede .depot/actions (source order), got:\n%s", content)
	}
}

func TestTransformWorkflow_SparseCheckoutSuperset_PartialMigration(t *testing.T) {
	// Under a partial migration the sparse-checkout superset must only point at
	// .depot/ paths that actually exist post-migration. .github/actions always
	// migrates, and a bare .github/workflows directory holds the migrated content,
	// so both gain a .depot/ sibling. A specific workflow file that was NOT copied
	// (other.yml) must not gain a .depot/workflows/other.yml sibling — that path
	// would not exist — while a migrated one (ci.yml) must.
	raw := []byte("name: CI\non: push\njobs:\n  build:\n    runs-on: depot-ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n        with:\n          sparse-checkout: |\n            .github/workflows\n            .github/workflows/ci.yml\n            .github/workflows/other.yml\n            .github/actions\n")

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}
	report := compat.AnalyzeWorkflow(wf)

	// Only ci.yml was migrated; other.yml stays at .github/.
	migratedWorkflows := map[string]bool{"ci.yml": true}

	result, err := TransformWorkflow(raw, wf, report, migratedWorkflows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	// The bare workflows directory, the migrated file, and actions all gain siblings.
	if !strings.Contains(content, ".depot/workflows\n") && !strings.Contains(content, ".depot/workflows ") {
		t.Errorf("expected bare .depot/workflows directory added, got:\n%s", content)
	}
	if !strings.Contains(content, ".depot/workflows/ci.yml") {
		t.Errorf("expected migrated .depot/workflows/ci.yml added, got:\n%s", content)
	}
	if !strings.Contains(content, ".depot/actions") {
		t.Errorf("expected .depot/actions added, got:\n%s", content)
	}
	// The non-migrated file must NOT gain a .depot/ sibling.
	if strings.Contains(content, ".depot/workflows/other.yml") {
		t.Errorf("expected NO .depot/workflows/other.yml sibling for un-migrated file, got:\n%s", content)
	}
	// Every original .github/ entry is preserved either way.
	for _, orig := range []string{".github/workflows/ci.yml", ".github/workflows/other.yml", ".github/actions"} {
		if !strings.Contains(content, orig) {
			t.Errorf("expected original %q preserved, got:\n%s", orig, content)
		}
	}
}

func TestTransformWorkflow_SparseCheckoutSuperset_PartialMigrationGlob(t *testing.T) {
	// A glob sparse-checkout pattern is self-limiting: even under a partial migration it
	// must gain its .depot/ sibling so the migrated workflow files that landed under
	// .depot/workflows are still checked out. Unlike a literal un-migrated filename, a
	// glob asserts no specific file, so it is not gated on the allow-list.
	raw := []byte("name: CI\non: push\njobs:\n  build:\n    runs-on: depot-ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n        with:\n          sparse-checkout-cone-mode: false\n          sparse-checkout: |\n            .github/workflows/*.yml\n")

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}
	report := compat.AnalyzeWorkflow(wf)

	// Only ci.yml was migrated, yet the glob must still be mirrored.
	migratedWorkflows := map[string]bool{"ci.yml": true}

	result, err := TransformWorkflow(raw, wf, report, migratedWorkflows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(result.Content)
	if !strings.Contains(content, ".depot/workflows/*.yml") {
		t.Errorf("expected glob .depot/workflows/*.yml sibling added, got:\n%s", content)
	}
	if !strings.Contains(content, ".github/workflows/*.yml") {
		t.Errorf("expected original glob preserved, got:\n%s", content)
	}
}
