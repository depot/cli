package tests

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadCandidatesReadsFromStdin(t *testing.T) {
	candidates, err := loadCandidates(context.Background(), strings.NewReader(" a.test.ts \n\nb.test.ts\r\n"), "", "", io.Discard)
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

func TestLoadCandidatesReadsFile(t *testing.T) {
	path := writeTempFile(t, "tests.txt", "from-file\n")

	candidates, err := loadCandidates(context.Background(), strings.NewReader(""), path, "", io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"from-file"}
	if !equalStrings(candidates, want) {
		t.Fatalf("expected candidates %v, got %v", want, candidates)
	}
}

func TestLoadCandidatesReadsCommandStdout(t *testing.T) {
	previous := runCandidatesCommandFunc
	runCandidatesCommandFunc = func(_ context.Context, command string, stdout, stderr io.Writer) error {
		if command != "discover-tests" {
			t.Fatalf("expected command %q, got %q", "discover-tests", command)
		}
		_, _ = io.WriteString(stdout, " a.test.ts \n\nb.test.ts\r\n")
		return nil
	}
	t.Cleanup(func() { runCandidatesCommandFunc = previous })

	candidates, err := loadCandidates(context.Background(), strings.NewReader(""), "", "discover-tests", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.test.ts", "b.test.ts"}; !equalStrings(candidates, want) {
		t.Fatalf("expected candidates %v, got %v", want, candidates)
	}
}

func TestLoadCandidatesDoesNotUseCommandOutputAfterFailure(t *testing.T) {
	previous := runCandidatesCommandFunc
	runCandidatesCommandFunc = func(_ context.Context, _ string, stdout, stderr io.Writer) error {
		_, _ = io.WriteString(stdout, "partial.test.ts\n")
		_, _ = io.WriteString(stderr, "discovery failed\n")
		return errors.New("exit status 1")
	}
	t.Cleanup(func() { runCandidatesCommandFunc = previous })

	var stderr strings.Builder
	candidates, err := loadCandidates(context.Background(), strings.NewReader(""), "", "discover-tests", &stderr)
	if err == nil || !strings.Contains(err.Error(), "candidate command failed") {
		t.Fatalf("expected candidate command failure, got candidates %v and error %v", candidates, err)
	}
	if candidates != nil {
		t.Fatalf("expected no partial candidates, got %v", candidates)
	}
	if stderr.String() != "discovery failed\n" {
		t.Fatalf("expected command stderr, got %q", stderr.String())
	}
}

func TestRunCandidatesCommandForwardsStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell utilities")
	}

	var stdout, stderr strings.Builder
	err := runCandidatesCommand(context.Background(), "printf 'a.test.ts\\n'; printf 'discovery diagnostic\\n' >&2", &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "a.test.ts\n" {
		t.Fatalf("expected command stdout, got %q", stdout.String())
	}
	if stderr.String() != "discovery diagnostic\n" {
		t.Fatalf("expected command stderr, got %q", stderr.String())
	}
}
