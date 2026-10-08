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
	err := workflows(opts)
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
	err := workflows(opts)
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

	err := workflows(opts)
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

	err := workflows(opts)
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

	err := workflows(opts)
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
	if !strings.Contains(output, "depot ci migrate secrets-and-vars`") {
		t.Errorf("expected the default GitHub follow-up command, got:\n%s", output)
	}
	if strings.Contains(output, "--forge=origin") {
		t.Errorf("default GitHub migration unexpectedly selected Cursor, got:\n%s", output)
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

	err := workflows(opts)
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

func TestRunMigrate_UnavailableRunnerNeedsReview(t *testing.T) {
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	os.MkdirAll(workflowsDir, 0755)

	legacy := `name: Legacy
on: push
jobs:
  build:
    runs-on: ubuntu-20.04
    steps:
      - run: make
`
	// A disabled job still takes priority in the status line, but the kept runner must be listed.
	mixed := `name: Mixed
on: push
jobs:
  test:
    runs-on: ubuntu-20.04
    steps:
      - run: make test
  deploy:
    runs-on: [self-hosted, linux]
    strategy:
      matrix:
        env: [staging, prod]
    steps:
      - run: make deploy
`
	os.WriteFile(filepath.Join(workflowsDir, "legacy.yml"), []byte(legacy), 0644)
	os.WriteFile(filepath.Join(workflowsDir, "mixed.yml"), []byte(mixed), 0644)

	var buf bytes.Buffer
	opts := migrateOptions{
		yes:    true,
		dir:    dir,
		stdout: &buf,
	}

	if err := workflows(opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	original, err := os.ReadFile(filepath.Join(workflowsDir, "legacy.yml"))
	if err != nil || string(original) != legacy {
		t.Errorf("expected .github workflow unchanged, got %q (err %v)", original, err)
	}
	migrated, err := os.ReadFile(filepath.Join(dir, ".depot", "workflows", "legacy.yml"))
	if err != nil {
		t.Fatalf("expected output file: %v", err)
	}
	if !strings.Contains(string(migrated), "runs-on: ubuntu-20.04 # kept: ubuntu-20.04.") {
		t.Errorf("expected ubuntu-20.04 kept with a review note, got:\n%s", migrated)
	}
	if strings.Contains(string(migrated), "depot-ubuntu-20.04") || strings.Contains(string(migrated), "runs-on: depot-ubuntu-latest") {
		t.Errorf("expected no invented or upgraded runner, got:\n%s", migrated)
	}

	output := buf.String()
	for _, want := range []string{
		"legacy.yml — 1 runner warning(s) (needs review)",
		`Job "build" uses runs-on "ubuntu-20.04"`,
		"mixed.yml — 1 job(s) disabled (needs review)",
		`Job "test" uses runs-on "ubuntu-20.04"`,
		"Choose a supported Depot runner label for this job before activating the workflow.",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in summary, got:\n%s", want, output)
		}
	}
	if strings.Contains(output, "migrated as is") {
		t.Errorf("expected kept runner not to be reported as migrated as is, got:\n%s", output)
	}
}

func TestRunMigrate_RunnerWarnings(t *testing.T) {
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	os.MkdirAll(workflowsDir, 0755)

	runsOn := map[string]string{
		"macos.yml":          `depot-macos-latest`,
		"windows.yml":        `depot-windows-latest`,
		"large.yml":          `depot-ubuntu-latest-96`,
		"unknown.yml":        `depot-made-up`,
		"conflicting.yml":    `["depot-ubuntu-24.04", "depot-ubuntu-22.04"]`,
		"dynamic_suffix.yml": `"depot-ubuntu-latest-96,dagger=${{ matrix.version }}"`,
		"legacy.yml":         `ubuntu-20.04`,
		"native.yml":         `depot-ubuntu-24.04`,
		"expression.yml":     `${{ matrix.runner }}`,
		"secondary.yml":      `["depot-ubuntu-24.04", "depot-made-up"]`,
		"latest.yml":         `ubuntu-latest`,
	}
	originals := make(map[string]string)
	for name, labels := range runsOn {
		originals[name] = "name: CI\non: push\njobs:\n  build:\n    runs-on: " + labels + "\n    steps:\n      - run: make\n"
		os.WriteFile(filepath.Join(workflowsDir, name), []byte(originals[name]), 0644)
	}

	var buf bytes.Buffer
	if err := workflows(migrateOptions{yes: true, dir: dir, stdout: &buf}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := buf.String()

	// summary returns a workflow's status line and the warning lines under it.
	summary := func(name string) string {
		_, rest, found := strings.Cut(output, "  "+name+" — ")
		if !found {
			t.Fatalf("expected %s in summary, got:\n%s", name, output)
		}
		lines := strings.Split(rest, "\n")
		block := lines[0]
		for _, line := range lines[1:] {
			if !strings.HasPrefix(line, "    ") {
				break
			}
			block += "\n" + line
		}
		return block
	}

	for _, name := range []string{"macos.yml", "windows.yml", "large.yml", "unknown.yml", "conflicting.yml", "dynamic_suffix.yml", "legacy.yml"} {
		got := summary(name)
		if !strings.HasPrefix(got, "1 runner warning(s) (needs review)") ||
			!strings.Contains(got, `Job "build" uses runs-on`) ||
			!strings.Contains(got, "runner label") {
			t.Errorf("expected %s to warn about job build with a suggested runner, got:\n%s", name, got)
		}
	}
	for _, name := range []string{"native.yml", "expression.yml", "secondary.yml", "latest.yml"} {
		if got := summary(name); strings.Contains(got, "needs review") {
			t.Errorf("expected no runner warning for %s, got:\n%s", name, got)
		}
	}

	for name, original := range originals {
		got, err := os.ReadFile(filepath.Join(workflowsDir, name))
		if err != nil || string(got) != original {
			t.Errorf("expected .github/workflows/%s unchanged, got %q (err %v)", name, got, err)
		}
		migrated, err := os.ReadFile(filepath.Join(dir, ".depot", "workflows", name))
		if err != nil {
			t.Fatalf("expected output file: %v", err)
		}
		switch name {
		case "latest.yml":
			if !strings.Contains(string(migrated), "runs-on: depot-ubuntu-latest") {
				t.Errorf("expected %s rewritten to depot-ubuntu-latest, got:\n%s", name, migrated)
			}
		case "legacy.yml":
		default:
			if !strings.HasSuffix(string(migrated), "\n"+original) {
				t.Errorf("expected %s migrated as is, got:\n%s", name, migrated)
			}
		}
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

	err := workflows(opts)
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

	err := workflows(opts)
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

func TestRunMigrate_CopiesWorkflowSiblings(t *testing.T) {
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	os.MkdirAll(filepath.Join(workflowsDir, "scripts"), 0755)

	workflow := `name: CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: bash .github/workflows/scripts/build.sh
`
	os.WriteFile(filepath.Join(workflowsDir, "ci.yml"), []byte(workflow), 0644)
	// Non-YAML sibling referenced by the workflow.
	os.WriteFile(filepath.Join(workflowsDir, "scripts", "build.sh"), []byte("#!/bin/sh\necho building\n"), 0755)

	var buf bytes.Buffer
	opts := migrateOptions{yes: true, dir: dir, stdout: &buf}
	if err := workflows(opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The sibling script should be mirrored into .depot/workflows/scripts/.
	sibling := filepath.Join(dir, ".depot", "workflows", "scripts", "build.sh")
	if data, err := os.ReadFile(sibling); err != nil {
		t.Fatalf("expected sibling copied to %s: %v", sibling, err)
	} else if !strings.Contains(string(data), "echo building") {
		t.Errorf("sibling content not preserved: %q", data)
	}

	// The workflow reference to the script should now point at .depot/.
	ci, err := os.ReadFile(filepath.Join(dir, ".depot", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ci), ".depot/workflows/scripts/build.sh") {
		t.Errorf("expected script reference rewritten to .depot/, got:\n%s", ci)
	}
}
