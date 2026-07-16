package gocache

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRemoteCachePutSkipsRemoteUploadWhenDiskAlreadyHasEntry(t *testing.T) {
	var puts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("unexpected method %s", r.Method)
		}
		puts.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	dir := t.TempDir()
	newCache := func() (*RemoteCache, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		return &RemoteCache{
			BaseURL:   server.URL,
			Token:     "token",
			Disk:      &DiskCache{Dir: dir},
			Ctx:       ctx,
			CtxCancel: cancel,
		}, cancel
	}

	cache, cancel := newCache()
	_, err := cache.Put(context.Background(), "abc123", "def456", 4, bytes.NewReader([]byte("data")))
	if err != nil {
		t.Fatalf("first Put returned error: %v", err)
	}
	_ = cache.Close()
	cancel()

	cache, cancel = newCache()
	_, err = cache.Put(context.Background(), "abc123", "def456", 4, bytes.NewReader([]byte("data")))
	if err != nil {
		t.Fatalf("second Put returned error: %v", err)
	}
	_ = cache.Close()
	cancel()

	if got := puts.Load(); got != 1 {
		t.Fatalf("remote PUT count = %d, want 1", got)
	}
}

func TestDiskCachePutReportsNoWriteForExistingEntry(t *testing.T) {
	dc := &DiskCache{Dir: t.TempDir()}

	_, wrote, err := dc.Put(context.Background(), "abc123", "def456", 4, bytes.NewReader([]byte("data")))
	if err != nil {
		t.Fatalf("first Put returned error: %v", err)
	}
	if !wrote {
		t.Fatal("first Put reported no write")
	}

	_, wrote, err = dc.Put(context.Background(), "abc123", "def456", 4, bytes.NewReader([]byte("data")))
	if err != nil {
		t.Fatalf("second Put returned error: %v", err)
	}
	if wrote {
		t.Fatal("second Put reported write")
	}
}
