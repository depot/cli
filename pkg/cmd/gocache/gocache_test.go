package gocache

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestValidOutputID(t *testing.T) {
	valid := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if !validOutputID(valid) {
		t.Fatal("valid output ID rejected")
	}
	if !validOutputID("E" + valid[1:]) {
		t.Fatal("uppercase output ID rejected")
	}
	if !validOutputID(strings.Repeat("ab", 127)) {
		t.Fatal("long output ID rejected")
	}

	invalid := []string{
		"../../tmp/some/path",
		"a",
		strings.Repeat("ab", 128),
	}
	for _, outputID := range invalid {
		if validOutputID(outputID) {
			t.Errorf("invalid output ID accepted: %q", outputID)
		}
	}
}

func TestRemoteCacheReusesRedirectConnections(t *testing.T) {
	const outputID = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	data := []byte("cached output")
	var body bytes.Buffer
	body.WriteByte(byte(len(outputID)))
	body.WriteString(outputID)
	_ = binary.Write(&body, binary.LittleEndian, uint32(len(data)))
	body.Write(data)

	const concurrency = 16
	var inFlight sync.WaitGroup
	var newConns atomic.Int64
	cdn := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold every request of a round open so the round needs one connection each.
		inFlight.Done()
		inFlight.Wait()
		_, _ = w.Write(body.Bytes())
	}))
	cdn.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConns.Add(1)
		}
	}
	cdn.Start()
	defer cdn.Close()

	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, cdn.URL+r.URL.Path, http.StatusSeeOther)
	}))
	defer cache.Close()

	const rounds = 3
	for round := range rounds {
		inFlight.Add(concurrency)
		var wg sync.WaitGroup
		for i := range concurrency {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rc := &RemoteCache{BaseURL: cache.URL, Disk: &DiskCache{Dir: t.TempDir()}}
				actionID := fmt.Sprintf("%064x", round*concurrency+i)
				if err := makeShardDirs(rc.Disk.Dir, actionID, outputID); err != nil {
					t.Error(err)
					return
				}
				got, _, err := rc.Get(context.Background(), actionID)
				if err != nil || got != outputID {
					t.Errorf("Get(%s) = %q, %v", actionID, got, err)
				}
			}()
		}
		wg.Wait()
	}

	if n := newConns.Load(); n > concurrency {
		t.Fatalf("opened %d connections to the redirect target over %d rounds of %d requests, want at most %d", n, rounds, concurrency, concurrency)
	}
}

func makeShardDirs(dir string, ids ...string) error {
	for _, id := range ids {
		if err := os.MkdirAll(filepath.Join(dir, id[:2]), 0755); err != nil {
			return err
		}
	}
	return nil
}
