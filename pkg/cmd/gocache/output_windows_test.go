package gocache

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var platformNoReplaceOps = map[string]noReplaceOps{
	"MoveFileExW and LockFileEx": {renameNoReplace: moveFileNoReplace, replace: replaceFile, lock: lockFile},
}

// A reader that has the output open, like vet.exe, must not stop another
// writer from finishing, and the writer must not disturb the reader.
func TestMoveFileNoReplaceWithOpenReader(t *testing.T) {
	dir := t.TempDir()
	to := filepath.Join(dir, "to")
	if err := os.WriteFile(to, []byte("output"), 0644); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(to)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	from := filepath.Join(dir, "from")
	if err := os.WriteFile(from, []byte("output"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := moveFileNoReplace(from, to); !errors.Is(err, os.ErrExist) {
		t.Fatalf("move onto an open file: err = %v, want os.ErrExist", err)
	}

	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "output" {
		t.Fatalf("reader got %q, %v", got, err)
	}
	assertContent(t, to, []byte("output"))
}

// A GOCACHE path can be longer than MAX_PATH. os.Rename handles that with the
// \\?\ prefix, so moveFileNoReplace must too, whether or not long paths are
// enabled on the system.
func TestMoveFileNoReplaceLongPath(t *testing.T) {
	dir := t.TempDir()
	for len(dir) < 300 {
		dir = filepath.Join(dir, strings.Repeat("d", 50))
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(dir, "from")
	to := filepath.Join(dir, "to")
	if err := os.WriteFile(from, []byte("output"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := moveFileNoReplace(from, to); err != nil {
		t.Fatalf("move with a long path: %v", err)
	}
	assertContent(t, to, []byte("output"))

	if err := os.WriteFile(from, []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := moveFileNoReplace(from, to); !errors.Is(err, os.ErrExist) {
		t.Fatalf("move onto an existing file with a long path: err = %v, want os.ErrExist", err)
	}
	assertContent(t, to, []byte("output"))
}

func TestExtendedPath(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ in, want string }{
		{`C:\cache\00\file`, `\\?\C:\cache\00\file`},
		{`C:/cache/./00/../00/file`, `\\?\C:\cache\00\file`},
		{`\\server\share\cache\file`, `\\?\UNC\server\share\cache\file`},
		{`\\?\C:\cache\file`, `\\?\C:\cache\file`},
		{`\??\C:\cache\file`, `\??\C:\cache\file`},
		{`\\.\pipe\name`, `\\.\pipe\name`},
		{`rel\file`, `\\?\` + filepath.Join(wd, `rel\file`)},
	}
	for _, tt := range tests {
		if got := extendedPath(tt.in); got != tt.want {
			t.Errorf("extendedPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Other depot writers hash a corrupt output without holding the lock, and Go
// opens files without FILE_SHARE_DELETE. A repair must wait for them instead of
// failing with "Access is denied".
func TestReplaceFileWithOpenChecker(t *testing.T) {
	dir := t.TempDir()
	to := filepath.Join(dir, "to")
	from := filepath.Join(dir, "from")
	if err := os.WriteFile(to, []byte("corrupt"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(from, []byte("output"), 0644); err != nil {
		t.Fatal(err)
	}
	checker, err := os.Open(to)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, to); !isSharingError(err) {
		checker.Close()
		t.Fatalf("plain rename over an open file: err = %v, want a sharing error", err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		checker.Close()
	}()

	if err := replaceFile(from, to); err != nil {
		t.Fatalf("replace while a checker has the file open: %v", err)
	}
	assertContent(t, to, []byte("output"))
}
