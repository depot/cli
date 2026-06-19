package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSplitModeDefaultsToTimings(t *testing.T) {
	mode, err := parseSplitMode("")
	if err != nil {
		t.Fatal(err)
	}
	if mode != splitModeTimings {
		t.Fatalf("expected default split mode %q, got %q", splitModeTimings, mode)
	}
}

func TestPartitionCandidatesByName(t *testing.T) {
	candidates := []string{"d", "a", "c", "b"}

	left, err := partitionCandidates(candidates, splitModeName, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	right, err := partitionCandidates(candidates, splitModeName, 1, 2)
	if err != nil {
		t.Fatal(err)
	}

	if !equalStrings(left, []string{"a", "c"}) {
		t.Fatalf("unexpected shard 0 candidates: %v", left)
	}
	if !equalStrings(right, []string{"d", "b"}) {
		t.Fatalf("unexpected shard 1 candidates: %v", right)
	}
	if hasOverlap(left, right) {
		t.Fatalf("expected non-overlapping shards, got %v and %v", left, right)
	}
}

func TestPartitionCandidatesByFileSize(t *testing.T) {
	dir := t.TempDir()
	small := writeSizedFile(t, dir, "small.test.ts", 1)
	medium := writeSizedFile(t, dir, "medium.test.ts", 10)
	large := writeSizedFile(t, dir, "large.test.ts", 100)
	otherLarge := writeSizedFile(t, dir, "other-large.test.ts", 90)

	selected, err := partitionCandidates([]string{small, medium, large, otherLarge}, splitModeFileSize, 0, 2)
	if err != nil {
		t.Fatal(err)
	}

	if !equalStrings(selected, []string{small, large}) {
		t.Fatalf("expected smallest and largest files in input order in shard 0, got %v", selected)
	}
}

func TestPartitionCandidatesTotalOneReturnsAllWithoutStat(t *testing.T) {
	candidates := []string{"missing-a.test.ts", "missing-b.test.ts"}

	selected, err := partitionCandidates(candidates, splitModeFileSize, 0, 1)
	if err != nil {
		t.Fatal(err)
	}

	if !equalStrings(selected, candidates) {
		t.Fatalf("expected all candidates unchanged, got %v", selected)
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
		{name: "negative index", index: -1, total: 2, want: "--index must be greater than or equal to 0"},
		{name: "index equal total", index: 2, total: 2, want: "--index must be less than --total"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := partitionCandidates([]string{"a"}, splitModeName, tt.index, tt.total)
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

	_, err := partitionCandidates([]string{filepath.Join(dir, "missing.test.ts")}, splitModeFileSize, 0, 2)
	if err == nil || !strings.Contains(err.Error(), "failed to stat candidate") {
		t.Fatalf("expected missing file error, got %v", err)
	}

	_, err = partitionCandidates([]string{dir}, splitModeFileSize, 0, 2)
	if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("expected regular file error, got %v", err)
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

func hasOverlap(left, right []string) bool {
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := values[value]; ok {
			return true
		}
	}
	return false
}
