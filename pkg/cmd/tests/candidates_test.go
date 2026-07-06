package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCandidatesReadsFromStdin(t *testing.T) {
	candidates, err := loadCandidates(strings.NewReader(" a.test.ts \n\nb.test.ts\r\n"), "")
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"a.test.ts", "b.test.ts"}
	if !equalStrings(candidates, want) {
		t.Fatalf("expected candidates %v, got %v", want, candidates)
	}
}

func writeTempFile(t *testing.T, name, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestLoadCandidatesFileTakesPrecedence(t *testing.T) {
	path := writeTempFile(t, "tests.txt", "from-file\n")

	candidates, err := loadCandidates(strings.NewReader("from-stdin\n"), path)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"from-file"}
	if !equalStrings(candidates, want) {
		t.Fatalf("expected candidates %v, got %v", want, candidates)
	}
}
