package ci

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMigrate_NoGitHub(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	opts := migrateOptions{
		yes:    true,
		dir:    dir,
		stdout: &buf,
	}
	err := copyWorkflows(opts)
	if err == nil {
		t.Fatal("expected error for missing .github directory")
	}
	if !strings.Contains(err.Error(), "no .github directory") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunMigrate_NoWorkflows(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0755)
	var buf bytes.Buffer
	opts := migrateOptions{
		yes:    true,
		dir:    dir,
		stdout: &buf,
	}
	err := copyWorkflows(opts)
	if err == nil {
		t.Fatal("expected error for no workflow files")
	}
	if !strings.Contains(err.Error(), "no valid workflow files") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunMigrate_BasicMigration(t *testing.T) {
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	os.MkdirAll(workflowsDir, 0755)

	workflow := `name: CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: echo "hello"
`
	os.WriteFile(filepath.Join(workflowsDir, "ci.yml"), []byte(workflow), 0644)

	var buf bytes.Buffer
	opts := migrateOptions{
		yes:    true,
		dir:    dir,
		stdout: &buf,
	}

	err := copyWorkflows(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check output file exists
	destPath := filepath.Join(dir, ".depot", "workflows", "ci.yml")
	content, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("expected output file at %s: %v", destPath, err)
	}

	contentStr := string(content)

	// Should have header
	if !strings.Contains(contentStr, "Depot CI Migration") {
		t.Error("expected migration header in output")
	}

	// Should have mapped runs-on
	if !strings.Contains(contentStr, "depot-ubuntu-latest") {
		t.Error("expected depot-ubuntu-latest in output")
	}

	// Should have comment about original label
	if !strings.Contains(contentStr, "was: ubuntu-latest") {
		t.Error("expected original label comment in output")
	}

	// Summary output
	output := buf.String()
	if !strings.Contains(output, "Migrated 1 workflow(s)") {
		t.Errorf("expected migration summary, got:\n%s", output)
	}
	if !strings.Contains(output, "Next steps:") {
		t.Errorf("expected next steps, got:\n%s", output)
	}
}

func TestRunMigrate_WithUnsupportedTrigger(t *testing.T) {
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	os.MkdirAll(workflowsDir, 0755)

	workflow := `name: Release
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
`
	os.WriteFile(filepath.Join(workflowsDir, "release.yml"), []byte(workflow), 0644)

	var buf bytes.Buffer
	opts := migrateOptions{
		yes:    true,
		dir:    dir,
		stdout: &buf,
	}

	err := copyWorkflows(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	destPath := filepath.Join(dir, ".depot", "workflows", "release.yml")
	content, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("expected output file: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "Removed unsupported trigger") {
		t.Error("expected unsupported trigger comment")
	}
	if !strings.Contains(contentStr, "Changes made:") {
		t.Error("expected changes header")
	}
}

func TestRunMigrate_WithSecrets(t *testing.T) {
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	os.MkdirAll(workflowsDir, 0755)

	workflow := `name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: echo ${{ secrets.MY_SECRET }}
    env:
      API_KEY: ${{ secrets.API_KEY }}
      MY_VAR: ${{ vars.MY_VAR }}
`
	os.WriteFile(filepath.Join(workflowsDir, "ci.yml"), []byte(workflow), 0644)

	var buf bytes.Buffer
	opts := migrateOptions{
		yes:    true,
		dir:    dir,
		stdout: &buf,
	}

	err := copyWorkflows(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "secret(s)") {
		t.Errorf("expected secrets count in summary, got:\n%s", output)
	}
	if !strings.Contains(output, "variable(s)") {
		t.Errorf("expected variables count in summary, got:\n%s", output)
	}
}

func TestRunMigrate_DisabledJob(t *testing.T) {
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	os.MkdirAll(workflowsDir, 0755)

	workflow := `name: CI
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
`
	os.WriteFile(filepath.Join(workflowsDir, "ci.yml"), []byte(workflow), 0644)

	var buf bytes.Buffer
	opts := migrateOptions{
		yes:    true,
		dir:    dir,
		stdout: &buf,
	}

	err := copyWorkflows(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	destPath := filepath.Join(dir, ".depot", "workflows", "ci.yml")
	content, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("expected output file: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "# DISABLED:") {
		t.Error("expected DISABLED comment for deploy job")
	}

	output := buf.String()
	if !strings.Contains(output, "disabled") {
		t.Errorf("expected disabled job in summary, got:\n%s", output)
	}
}

func TestRunMigrate_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	os.MkdirAll(workflowsDir, 0755)
	os.MkdirAll(filepath.Join(dir, ".depot", "workflows"), 0755)

	workflow := `name: CI
on: push
jobs:
  build:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
`
	os.WriteFile(filepath.Join(workflowsDir, "ci.yml"), []byte(workflow), 0644)

	var buf bytes.Buffer
	opts := migrateOptions{
		yes:       true,
		overwrite: true,
		dir:       dir,
		stdout:    &buf,
	}

	err := copyWorkflows(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	destPath := filepath.Join(dir, ".depot", "workflows", "ci.yml")
	if _, err := os.Stat(destPath); os.IsNotExist(err) {
		t.Error("expected output file to exist after overwrite")
	}
}

func TestRunMigrate_MultipleWorkflows(t *testing.T) {
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	os.MkdirAll(workflowsDir, 0755)

	wf1 := `name: CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
`
	wf2 := `name: Deploy
on: push
jobs:
  deploy:
    runs-on: depot-ubuntu-latest
    steps:
      - uses: actions/checkout@v4
`
	os.WriteFile(filepath.Join(workflowsDir, "ci.yml"), []byte(wf1), 0644)
	os.WriteFile(filepath.Join(workflowsDir, "deploy.yml"), []byte(wf2), 0644)

	var buf bytes.Buffer
	opts := migrateOptions{
		yes:    true,
		dir:    dir,
		stdout: &buf,
	}

	err := copyWorkflows(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Migrated 2 workflow(s)") {
		t.Errorf("expected 2 workflows migrated, got:\n%s", output)
	}

	// Both output files should exist
	for _, name := range []string{"ci.yml", "deploy.yml"} {
		destPath := filepath.Join(dir, ".depot", "workflows", name)
		if _, err := os.Stat(destPath); os.IsNotExist(err) {
			t.Errorf("expected output file %s", name)
		}
	}
}
