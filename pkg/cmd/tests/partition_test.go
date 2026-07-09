package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPartitionCandidatesByFileSize(t *testing.T) {
	dir := t.TempDir()
	small := writeSizedFile(t, dir, "small.test.ts", 1)
	medium := writeSizedFile(t, dir, "medium.test.ts", 10)
	large := writeSizedFile(t, dir, "large.test.ts", 100)
	otherLarge := writeSizedFile(t, dir, "other-large.test.ts", 90)

	selected, err := partitionCandidatesByFileSize([]string{small, medium, large, otherLarge}, 0, 2)
	if err != nil {
		t.Fatal(err)
	}

	if !equalStrings(selected, []string{small, large}) {
		t.Fatalf("expected smallest and largest files in input order in shard 0, got %v", selected)
	}
}

func TestPartitionCandidatesTotalOneReturnsAllWithoutStat(t *testing.T) {
	candidates := []string{"missing-a.test.ts", "missing-b.test.ts"}

	selected, err := partitionCandidatesByFileSize(candidates, 0, 1)
	if err != nil {
		t.Fatal(err)
	}

	if !equalStrings(selected, candidates) {
		t.Fatalf("expected all candidates unchanged, got %v", selected)
	}
}

func TestPartitionCandidatesByFileSizeDeduplicatesCandidatePaths(t *testing.T) {
	dir := t.TempDir()
	large := writeSizedFile(t, dir, "large.test.ts", 100)
	medium := writeSizedFile(t, dir, "medium.test.ts", 90)
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	largePathVariant := filepath.Join(subdir, "..", "large.test.ts")

	shard0, err := partitionCandidatesByFileSize([]string{large, medium, largePathVariant}, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	shard1, err := partitionCandidatesByFileSize([]string{large, medium, largePathVariant}, 1, 2)
	if err != nil {
		t.Fatal(err)
	}

	if !equalStrings(shard0, []string{large}) {
		t.Fatalf("expected duplicate path to be selected once in shard 0, got %v", shard0)
	}
	if !equalStrings(shard1, []string{medium}) {
		t.Fatalf("expected medium path in shard 1, got %v", shard1)
	}
}

func TestPartitionCandidatesValidatesShard(t *testing.T) {
	tests := []struct {
		name  string
		index int
		total int
		want  string
	}{
		{name: "zero total", index: 0, total: 0, want: "--total must be greater than 0"},
		{name: "total too large", index: 0, total: maxShardTotal + 1, want: "--total must be <= 10000"},
		{name: "negative index", index: -1, total: 2, want: "--index must be greater than or equal to 0"},
		{name: "index equal total", index: 2, total: 2, want: "--index must be less than --total"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := partitionCandidatesByFileSize([]string{"a"}, tt.index, tt.total)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %q", tt.want, err.Error())
			}
		})
	}
}

func TestPartitionCandidatesFileSizeFailsForMissingOrDirectory(t *testing.T) {
	dir := t.TempDir()

	_, err := partitionCandidatesByFileSize([]string{filepath.Join(dir, "missing.test.ts")}, 0, 2)
	if err == nil || !strings.Contains(err.Error(), "failed to stat candidate") {
		t.Fatalf("expected missing file error, got %v", err)
	}

	_, err = partitionCandidatesByFileSize([]string{dir}, 0, 2)
	if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("expected regular file error, got %v", err)
	}
}

func TestPartitionCandidatesFileSizeRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := writeSizedFile(t, dir, "target.test.ts", 1)
	link := filepath.Join(dir, "link.test.ts")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := partitionCandidatesByFileSize([]string{link}, 0, 2)
	if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("expected symlink regular file error, got %v", err)
	}
}

func writeSizedFile(t *testing.T, dir, name string, size int) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
