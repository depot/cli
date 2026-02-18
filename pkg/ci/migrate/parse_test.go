package migrate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseWorkflowFileOnString(t *testing.T) {
	path := writeTempWorkflow(t, t.TempDir(), "string.yml", "name: CI\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n")

	wf, err := ParseWorkflowFile(path)
	if err != nil {
		t.Fatalf("ParseWorkflowFile failed: %v", err)
	}

	assertStringSliceEqual(t, wf.Triggers, []string{"push"})
}

func TestParseWorkflowFileOnList(t *testing.T) {
	path := writeTempWorkflow(t, t.TempDir(), "list.yml", "name: CI\non: [push, pull_request]\njobs:\n  build:\n    runs-on: ubuntu-latest\n")

	wf, err := ParseWorkflowFile(path)
	if err != nil {
		t.Fatalf("ParseWorkflowFile failed: %v", err)
	}

	assertStringSliceEqual(t, wf.Triggers, []string{"push", "pull_request"})
}

func TestParseWorkflowFileOnMap(t *testing.T) {
	path := writeTempWorkflow(t, t.TempDir(), "map.yml", "name: CI\non:\n  push:\n    branches: [main]\njobs:\n  build:\n    runs-on: ubuntu-latest\n")

	wf, err := ParseWorkflowFile(path)
	if err != nil {
		t.Fatalf("ParseWorkflowFile failed: %v", err)
	}

	assertStringSliceEqual(t, wf.Triggers, []string{"push"})
}

func TestParseWorkflowFileMatrix(t *testing.T) {
	path := writeTempWorkflow(t, t.TempDir(), "matrix.yml", "name: Matrix\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    strategy:\n      matrix:\n        go-version: [\"1.22\", \"1.23\"]\n")

	wf, err := ParseWorkflowFile(path)
	if err != nil {
		t.Fatalf("ParseWorkflowFile failed: %v", err)
	}

	if len(wf.Jobs) != 1 || !wf.Jobs[0].HasMatrix {
		t.Fatalf("expected job with matrix, got %+v", wf.Jobs)
	}
}

func TestParseWorkflowFileContainer(t *testing.T) {
	path := writeTempWorkflow(t, t.TempDir(), "container.yml", "name: Container\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    container: node:20\n")

	wf, err := ParseWorkflowFile(path)
	if err != nil {
		t.Fatalf("ParseWorkflowFile failed: %v", err)
	}

	if len(wf.Jobs) != 1 || !wf.Jobs[0].HasContainer {
		t.Fatalf("expected job with container, got %+v", wf.Jobs)
	}
}

func TestParseWorkflowFileServices(t *testing.T) {
	path := writeTempWorkflow(t, t.TempDir(), "services.yml", "name: Services\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    services:\n      redis:\n        image: redis:7\n")

	wf, err := ParseWorkflowFile(path)
	if err != nil {
		t.Fatalf("ParseWorkflowFile failed: %v", err)
	}

	if len(wf.Jobs) != 1 || !wf.Jobs[0].HasServices {
		t.Fatalf("expected job with services, got %+v", wf.Jobs)
	}
}

func TestParseWorkflowFileMultipleJobs(t *testing.T) {
	path := writeTempWorkflow(t, t.TempDir(), "multi.yml", "name: Multi\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n  test:\n    runs-on: [ubuntu-latest, self-hosted]\n")

	wf, err := ParseWorkflowFile(path)
	if err != nil {
		t.Fatalf("ParseWorkflowFile failed: %v", err)
	}

	if len(wf.Jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(wf.Jobs))
	}

	if wf.Jobs[0].Name != "build" || wf.Jobs[1].Name != "test" {
		t.Fatalf("expected build/test jobs, got %+v", wf.Jobs)
	}

	if wf.Jobs[1].RunsOn != "ubuntu-latest,self-hosted" {
		t.Fatalf("unexpected runs-on for test job: %q", wf.Jobs[1].RunsOn)
	}
}

func TestParseWorkflowFileReusableWorkflow(t *testing.T) {
	path := writeTempWorkflow(t, t.TempDir(), "reusable.yml", "name: Reusable\non: push\njobs:\n  call-reusable:\n    uses: owner/repo/.github/workflows/reusable.yml@main\n")

	wf, err := ParseWorkflowFile(path)
	if err != nil {
		t.Fatalf("ParseWorkflowFile failed: %v", err)
	}

	if len(wf.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(wf.Jobs))
	}

	if wf.Jobs[0].UsesReusable == "" {
		t.Fatalf("expected reusable workflow reference, got empty value")
	}
}

func TestParseWorkflowFileInvalidYAML(t *testing.T) {
	path := writeTempWorkflow(t, t.TempDir(), "invalid.yml", "name: Invalid\non: [push\njobs:\n  build:\n    runs-on: ubuntu-latest\n")

	if _, err := ParseWorkflowFile(path); err == nil {
		t.Fatal("expected invalid YAML error, got nil")
	}
}

func TestParseWorkflowFileEmptyFile(t *testing.T) {
	path := writeTempWorkflow(t, t.TempDir(), "empty.yml", "")

	if _, err := ParseWorkflowFile(path); err == nil {
		t.Fatal("expected empty file error, got nil")
	}
}

func TestParseWorkflowDirMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	writeTempWorkflow(t, dir, "one.yml", "name: One\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n")
	writeTempWorkflow(t, dir, "two.yaml", "name: Two\non: pull_request\njobs:\n  test:\n    runs-on: ubuntu-latest\n")
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("ignore"), 0644); err != nil {
		t.Fatalf("failed to write README.txt: %v", err)
	}

	workflows, err := ParseWorkflowDir(dir)
	if err != nil {
		t.Fatalf("ParseWorkflowDir failed: %v", err)
	}

	if len(workflows) != 2 {
		t.Fatalf("expected 2 parsed workflows, got %d", len(workflows))
	}
}

func writeTempWorkflow(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write workflow file %s: %v", path, err)
	}

	return path
}

func assertStringSliceEqual(t *testing.T, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("slice length mismatch: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slice mismatch at index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}
