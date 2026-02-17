package migrate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectSecretsFromFileSimple(t *testing.T) {
	path := writeTempTextFile(t, t.TempDir(), "simple.yml", "token: ${{ secrets.NPM_TOKEN }}\n")

	secrets, err := DetectSecretsFromFile(path)
	if err != nil {
		t.Fatalf("DetectSecretsFromFile failed: %v", err)
	}

	assertStringSliceEqual(t, secrets, []string{"NPM_TOKEN"})
}

func TestDetectSecretsFromFileWithDefaultExpression(t *testing.T) {
	path := writeTempTextFile(t, t.TempDir(), "default.yml", "token: ${{ secrets.X || 'default' }}\n")

	secrets, err := DetectSecretsFromFile(path)
	if err != nil {
		t.Fatalf("DetectSecretsFromFile failed: %v", err)
	}

	assertStringSliceEqual(t, secrets, []string{"X"})
}

func TestDetectSecretsFromFileNoFalsePositive(t *testing.T) {
	path := writeTempTextFile(t, t.TempDir(), "env.yml", "value: ${{ env.SOME_VAR }}\n")

	secrets, err := DetectSecretsFromFile(path)
	if err != nil {
		t.Fatalf("DetectSecretsFromFile failed: %v", err)
	}

	if len(secrets) != 0 {
		t.Fatalf("expected no secrets, got %v", secrets)
	}
}

func TestDetectVariablesFromFile(t *testing.T) {
	path := writeTempTextFile(t, t.TempDir(), "vars.yml", "value: ${{ vars.MY_VAR }}\n")

	variables, err := DetectVariablesFromFile(path)
	if err != nil {
		t.Fatalf("DetectVariablesFromFile failed: %v", err)
	}

	assertStringSliceEqual(t, variables, []string{"MY_VAR"})
}

func TestDetectSecretsFromFileMultipleDeduplicated(t *testing.T) {
	path := writeTempTextFile(t, t.TempDir(), "multi.yml", "a: ${{ secrets.A }}\nb: ${{ secrets.B }}\nc: ${{ secrets.A }}\n")

	secrets, err := DetectSecretsFromFile(path)
	if err != nil {
		t.Fatalf("DetectSecretsFromFile failed: %v", err)
	}

	assertStringSliceEqual(t, secrets, []string{"A", "B"})
}

func TestDetectSecretsFromDirDeduplicatesAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	writeTempTextFile(t, dir, "one.yml", "a: ${{ secrets.SHARED }}\n")
	writeTempTextFile(t, dir, "two.yaml", "b: ${{ secrets.SHARED }}\nc: ${{ secrets.OTHER }}\n")

	secrets, err := DetectSecretsFromDir(dir)
	if err != nil {
		t.Fatalf("DetectSecretsFromDir failed: %v", err)
	}

	assertStringSliceEqual(t, secrets, []string{"OTHER", "SHARED"})
}

func TestDetectSecretsFromFileGitHubToken(t *testing.T) {
	path := writeTempTextFile(t, t.TempDir(), "github-token.yml", "token: ${{ secrets.GITHUB_TOKEN }}\n")

	secrets, err := DetectSecretsFromFile(path)
	if err != nil {
		t.Fatalf("DetectSecretsFromFile failed: %v", err)
	}

	assertStringSliceEqual(t, secrets, []string{"GITHUB_TOKEN"})
}

func writeTempTextFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file %s: %v", path, err)
	}

	return path
}
