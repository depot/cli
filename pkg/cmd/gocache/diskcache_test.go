package gocache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// newTestDiskCache returns a DiskCache with the 00 to ff subdirectories that
// the go command creates in GOCACHE.
func newTestDiskCache(t *testing.T) *DiskCache {
	t.Helper()
	dir := t.TempDir()
	for i := range 256 {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("%02x", i)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return &DiskCache{Dir: dir}
}

// outputIDOf returns the output ID that the go command uses for content.
func outputIDOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func actionIDOf(name string) string {
	return outputIDOf([]byte("action " + name))
}

func putOutput(t *testing.T, dc *DiskCache, actionID string, content []byte) string {
	t.Helper()
	diskPath, err := dc.Put(context.Background(), actionID, outputIDOf(content), int64(len(content)), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("put %s: %v", actionID, err)
	}
	return diskPath
}

func assertCached(t *testing.T, dc *DiskCache, actionID string, content []byte) {
	t.Helper()
	outputID, diskPath, err := dc.Get(context.Background(), actionID)
	if err != nil {
		t.Fatalf("get %s: %v", actionID, err)
	}
	if outputID != outputIDOf(content) {
		t.Fatalf("get %s: outputID = %q, want %q", actionID, outputID, outputIDOf(content))
	}
	assertContent(t, diskPath, content)
}

func assertContent(t *testing.T, file string, content []byte) {
	t.Helper()
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("%s holds %q, want %q", file, got, content)
	}
}

func TestDiskCachePutAndGet(t *testing.T) {
	for _, content := range [][]byte{[]byte("compiled package"), {}} {
		dc := newTestDiskCache(t)
		actionID := actionIDOf("a")

		diskPath := putOutput(t, dc, actionID, content)
		if diskPath != fileNameOutput(dc.Dir, outputIDOf(content)) {
			t.Fatalf("put returned %q, want the output file", diskPath)
		}
		assertCached(t, dc, actionID, content)
	}
}

// Several actions can produce the same output, and setup-go can restore
// outputs from an earlier run. On Windows, putting another action for an output
// that is already on disk must not replace the file, because that breaks
// readers such as vet.exe that have it open.
func TestDiskCachePutSharedOutput(t *testing.T) {
	dc := newTestDiskCache(t)
	content := []byte("vet facts")

	first := putOutput(t, dc, actionIDOf("a"), content)
	before, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}

	second := putOutput(t, dc, actionIDOf("b"), content)
	if second != first {
		t.Fatalf("second put returned %q, want %q", second, first)
	}
	after, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && !os.SameFile(before, after) {
		t.Fatal("second put replaced the shared output file")
	}

	assertCached(t, dc, actionIDOf("a"), content)
	assertCached(t, dc, actionIDOf("b"), content)
}

func TestDiskCachePutRewritesBadOutput(t *testing.T) {
	content := []byte("compiled package")
	tests := []struct {
		name         string
		onDisk       []byte
		actionExists bool
	}{
		{"wrong size", []byte("truncated"), false},
		{"wrong size, action exists", []byte("truncated"), true},
		{"same size, wrong content", bytes.Repeat([]byte("x"), len(content)), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := newTestDiskCache(t)
			actionID := actionIDOf("a")
			if tt.actionExists {
				putOutput(t, dc, actionID, content)
			}
			if err := os.WriteFile(fileNameOutput(dc.Dir, outputIDOf(content)), tt.onDisk, 0644); err != nil {
				t.Fatal(err)
			}

			putOutput(t, dc, actionID, content)
			assertCached(t, dc, actionID, content)
		})
	}
}

// Many actions putting the same output at once all succeed. On Windows, only
// the first file may ever be published: replacing it later could break a reader
// that got its path from an earlier put.
func TestDiskCachePutConcurrentSharedOutput(t *testing.T) {
	const rounds, writers = 20, 16
	dc := newTestDiskCache(t)

	for round := range rounds {
		content := fmt.Appendf(nil, "vet facts %d", round)
		published := concurrently(t, writers, func(i int) (string, error) {
			actionID := actionIDOf(fmt.Sprintf("%d-%d", round, i))
			return dc.Put(context.Background(), actionID, outputIDOf(content), int64(len(content)), bytes.NewReader(content))
		})
		if runtime.GOOS == "windows" {
			assertSameFile(t, published)
		}
		for i := range writers {
			assertCached(t, dc, actionIDOf(fmt.Sprintf("%d-%d", round, i)), content)
		}
	}
}

func TestDiskCachePutReportsWriteError(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory that the test user cannot write to")
	}
	dc := newTestDiskCache(t)
	content := []byte("compiled package")
	outputDir := filepath.Dir(fileNameOutput(dc.Dir, outputIDOf(content)))
	if err := os.Chmod(outputDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outputDir, 0755) })

	_, err := dc.Put(context.Background(), actionIDOf("a"), outputIDOf(content), int64(len(content)), bytes.NewReader(content))
	if err == nil {
		t.Fatal("put succeeded without a writable output directory")
	}
}

// concurrently runs write in n goroutines that start at the same time, and
// returns the file each one reports right after it finishes.
func concurrently(t *testing.T, n int, write func(i int) (string, error)) []os.FileInfo {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var published []os.FileInfo
	var errs []error
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			file, err := write(i)
			var fi os.FileInfo
			if err == nil {
				fi, err = os.Stat(file)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			published = append(published, fi)
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		t.Fatalf("write: %v", err)
	}
	return published
}

func assertSameFile(t *testing.T, published []os.FileInfo) {
	t.Helper()
	for _, fi := range published[1:] {
		if !os.SameFile(published[0], fi) {
			t.Fatal("a writer replaced the output that another writer had already returned")
		}
	}
}

// Another GOCACHEPROG client may send output IDs that are not a SHA-256.
// Putting a second action for such an output that is already on disk succeeds.
func TestDiskCachePutUnhashableOutputID(t *testing.T) {
	dc := newTestDiskCache(t)
	const outputID = "abcd"
	content := []byte("compiled package")
	for _, action := range []string{"a", "b"} {
		_, err := dc.Put(context.Background(), actionIDOf(action), outputID, int64(len(content)), bytes.NewReader(content))
		if err != nil {
			t.Fatalf("put %s: %v", action, err)
		}
	}
	for _, action := range []string{"a", "b"} {
		gotID, diskPath, err := dc.Get(context.Background(), actionIDOf(action))
		if err != nil || gotID != outputID {
			t.Fatalf("get %s: outputID = %q, err = %v", action, gotID, err)
		}
		assertContent(t, diskPath, content)
	}
}

// A new action that reuses an output on disk reports a new entry time to the
// go command, which uses it to expire test results.
func TestDiskCachePutSharedOutputRefreshesTime(t *testing.T) {
	dc := newTestDiskCache(t)
	content := []byte("test output")
	first := putOutput(t, dc, actionIDOf("a"), content)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(first, old, old); err != nil {
		t.Fatal(err)
	}

	start := time.Now().Add(-time.Second)
	second := putOutput(t, dc, actionIDOf("b"), content)
	fi, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	if fi.ModTime().Before(start) {
		t.Fatalf("shared output has modification time %v, want after %v", fi.ModTime(), start)
	}
}
