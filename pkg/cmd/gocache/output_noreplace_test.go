package gocache

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// linkNoReplace stands in for MoveFileExW without MOVEFILE_REPLACE_EXISTING, so
// that writeOutputNoReplace is tested on every platform: a hard link also
// fails when the destination exists.
func linkNoReplace(from, to string) error {
	if err := os.Link(from, to); err != nil {
		return err
	}
	return os.Remove(from)
}

var testLockMu sync.Mutex

// mutexLock stands in for LockFileEx within one process.
func mutexLock(string) (func(), error) {
	testLockMu.Lock()
	return testLockMu.Unlock, nil
}

// noReplaceOpsToTest returns the implementations of noReplaceOps to test.
func noReplaceOpsToTest() map[string]noReplaceOps {
	all := map[string]noReplaceOps{"hard link": {renameNoReplace: linkNoReplace, replace: os.Rename, lock: mutexLock}}
	maps.Copy(all, platformNoReplaceOps)
	return all
}

func writeTestOutput(t *testing.T, dest string, content []byte, ops noReplaceOps) {
	t.Helper()
	wrote, err := writeOutputNoReplace(dest, outputIDOf(content), int64(len(content)), bytes.NewReader(content), ops)
	if err != nil {
		t.Fatalf("write output: %v", err)
	}
	if wrote != int64(len(content)) {
		t.Fatalf("wrote %d bytes, want %d", wrote, len(content))
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	leftovers, err := filepath.Glob(filepath.Join(dir, "*.*"))
	if err != nil {
		t.Fatal(err)
	}
	var temps []string
	for _, file := range leftovers {
		if filepath.Base(file) != outputLockName {
			temps = append(temps, file)
		}
	}
	if len(temps) > 0 {
		t.Fatalf("temporary files left behind: %v", temps)
	}
}

func TestRenameNoReplace(t *testing.T) {
	for name, ops := range noReplaceOpsToTest() {
		rename := ops.renameNoReplace
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			from := filepath.Join(dir, "from")
			to := filepath.Join(dir, "to")
			if err := os.WriteFile(from, []byte("new"), 0644); err != nil {
				t.Fatal(err)
			}

			if err := rename(from, to); err != nil {
				t.Fatalf("rename to a missing file: %v", err)
			}
			assertContent(t, to, []byte("new"))
			if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("from still exists after rename: %v", err)
			}

			if err := os.WriteFile(from, []byte("other"), 0644); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(to)
			if err != nil {
				t.Fatal(err)
			}
			if err := rename(from, to); !errors.Is(err, os.ErrExist) {
				t.Fatalf("rename onto an existing file: err = %v, want os.ErrExist", err)
			}
			after, err := os.Stat(to)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("rename onto an existing file replaced it")
			}
			assertContent(t, to, []byte("new"))
		})
	}
}

func TestWriteOutputNoReplaceKeepsExistingOutput(t *testing.T) {
	for name, ops := range noReplaceOpsToTest() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "output")
			content := []byte("vet facts")

			writeTestOutput(t, dest, content, ops)
			before, err := os.Stat(dest)
			if err != nil {
				t.Fatal(err)
			}
			writeTestOutput(t, dest, content, ops)
			after, err := os.Stat(dest)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("second write replaced the output")
			}
			assertContent(t, dest, content)
			assertNoTempFiles(t, dir)
		})
	}
}

func TestWriteOutputNoReplaceRewritesBadOutput(t *testing.T) {
	content := []byte("compiled package")
	bad := map[string][]byte{
		"wrong size":               []byte("truncated"),
		"same size, wrong content": bytes.Repeat([]byte("x"), len(content)),
	}
	for name, ops := range noReplaceOpsToTest() {
		for badName, onDisk := range bad {
			t.Run(name+"/"+badName, func(t *testing.T) {
				dir := t.TempDir()
				dest := filepath.Join(dir, "output")
				if err := os.WriteFile(dest, onDisk, 0644); err != nil {
					t.Fatal(err)
				}

				writeTestOutput(t, dest, content, ops)
				assertContent(t, dest, content)
				assertNoTempFiles(t, dir)
			})
		}
	}
}

// Writers that all find the same corrupt output race to repair it. Only the
// first repair may be published: a second repair would replace an output that
// the first writer has already returned.
func TestWriteOutputNoReplaceConcurrentRepair(t *testing.T) {
	const rounds, writers = 50, 16
	for name, ops := range noReplaceOpsToTest() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for round := range rounds {
				content := fmt.Appendf(nil, "vet facts %d", round)
				dest := filepath.Join(dir, fmt.Sprintf("output-%d", round))
				if err := os.WriteFile(dest, bytes.Repeat([]byte("x"), len(content)), 0644); err != nil {
					t.Fatal(err)
				}

				// Hold every writer after its first rename, so that all of them
				// find the corrupt file before any of them repairs it.
				var calls atomic.Int32
				var arrived sync.WaitGroup
				arrived.Add(writers)
				racing := ops
				racing.renameNoReplace = func(from, to string) error {
					err := ops.renameNoReplace(from, to)
					if calls.Add(1) <= writers {
						arrived.Done()
						arrived.Wait()
					}
					return err
				}

				published := concurrently(t, writers, func(int) (string, error) {
					_, err := writeOutputNoReplace(dest, outputIDOf(content), int64(len(content)), bytes.NewReader(content), racing)
					return dest, err
				})
				assertSameFile(t, published)
				assertContent(t, dest, content)
			}
			assertNoTempFiles(t, dir)
		})
	}
}

// A file that cannot be read may hold the right output, so it is never
// replaced. The write fails instead.
func TestWriteOutputNoReplaceKeepsUnreadableOutput(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a file that the test user cannot read")
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "output")
	content := []byte("vet facts")
	if err := os.WriteFile(dest, content, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dest, 0); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}

	ops := noReplaceOps{renameNoReplace: linkNoReplace, replace: os.Rename, lock: mutexLock}
	if _, err := writeOutputNoReplace(dest, outputIDOf(content), int64(len(content)), bytes.NewReader(content), ops); err == nil {
		t.Fatal("write succeeded without being able to check the existing output")
	}
	after, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("unreadable output was replaced")
	}
	assertNoTempFiles(t, dir)
}

// Writers that all find the output missing race to publish it. Only the first
// file may ever be published: replacing it later could break a reader that got
// its path from an earlier writer.
func TestWriteOutputNoReplaceConcurrent(t *testing.T) {
	const rounds, writers = 50, 16
	for name, ops := range noReplaceOpsToTest() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for round := range rounds {
				content := fmt.Appendf(nil, "vet facts %d", round)
				dest := filepath.Join(dir, fmt.Sprintf("output-%d", round))
				published := concurrently(t, writers, func(int) (string, error) {
					_, err := writeOutputNoReplace(dest, outputIDOf(content), int64(len(content)), bytes.NewReader(content), ops)
					return dest, err
				})
				assertSameFile(t, published)
				assertContent(t, dest, content)
			}
			assertNoTempFiles(t, dir)
		})
	}
}

// Output IDs that are not a SHA-256, which other GOCACHEPROG clients may send,
// cannot be hashed. A file of the right size is kept, and one of the wrong size
// is rewritten.
func TestWriteOutputNoReplaceUnhashableOutputID(t *testing.T) {
	const outputID = "abcd"
	content := []byte("compiled package")
	write := func(t *testing.T, dest string, ops noReplaceOps) {
		t.Helper()
		if _, err := writeOutputNoReplace(dest, outputID, int64(len(content)), bytes.NewReader(content), ops); err != nil {
			t.Fatalf("write output: %v", err)
		}
	}
	for name, ops := range noReplaceOpsToTest() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "output")

			write(t, dest, ops)
			before, err := os.Stat(dest)
			if err != nil {
				t.Fatal(err)
			}
			write(t, dest, ops)
			after, err := os.Stat(dest)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("second write replaced the output")
			}
			assertContent(t, dest, content)

			if err := os.WriteFile(dest, []byte("truncated"), 0644); err != nil {
				t.Fatal(err)
			}
			write(t, dest, ops)
			assertContent(t, dest, content)
			assertNoTempFiles(t, dir)
		})
	}
}

// The go command gets an entry's time from its output file, so an output that
// is kept for a new action must get a new modification time, as it would if it
// were rewritten. Otherwise test results look expired after go clean -testcache.
func TestWriteOutputNoReplaceRefreshesKeptOutput(t *testing.T) {
	content := []byte("test output")
	old := time.Now().Add(-48 * time.Hour)
	tests := map[string]func(t *testing.T, dest string, ops noReplaceOps) noReplaceOps{
		"already on disk": func(t *testing.T, dest string, ops noReplaceOps) noReplaceOps {
			writeTestOutput(t, dest, content, ops)
			if err := os.Chtimes(dest, old, old); err != nil {
				t.Fatal(err)
			}
			return ops
		},
		"written by another writer first": func(t *testing.T, dest string, ops noReplaceOps) noReplaceOps {
			racing := ops
			racing.renameNoReplace = func(from, to string) error {
				if err := os.WriteFile(to, content, 0644); err != nil {
					return err
				}
				if err := os.Chtimes(to, old, old); err != nil {
					return err
				}
				return ops.renameNoReplace(from, to)
			}
			return racing
		},
	}
	for name, ops := range noReplaceOpsToTest() {
		for testName, setup := range tests {
			t.Run(name+"/"+testName, func(t *testing.T) {
				dir := t.TempDir()
				dest := filepath.Join(dir, "output")
				writeOps := setup(t, dest, ops)

				start := time.Now().Add(-time.Second)
				writeTestOutput(t, dest, content, writeOps)
				fi, err := os.Stat(dest)
				if err != nil {
					t.Fatal(err)
				}
				if fi.ModTime().Before(start) {
					t.Fatalf("kept output has modification time %v, want after %v", fi.ModTime(), start)
				}
				assertContent(t, dest, content)
				assertNoTempFiles(t, dir)
			})
		}
	}
}

// A body that does not match the expected size is never published, so no later
// writer has to repair it.
func TestWriteOutputNoReplaceRejectsWrongSize(t *testing.T) {
	content := []byte("compiled package")
	for name, ops := range noReplaceOpsToTest() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "output")
			_, err := writeOutputNoReplace(dest, outputIDOf(content), int64(len(content)), bytes.NewReader(content[:4]), ops)
			if err == nil {
				t.Fatal("write of a short body succeeded")
			}
			if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("short body was published: %v", err)
			}
			assertNoTempFiles(t, dir)
		})
	}
}
