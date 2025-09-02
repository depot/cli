package fswatch

import (
	"context"
	"fmt"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestWatch(t *testing.T) {
	fileSystem := fstest.MapFS{
		"file1.txt": &fstest.MapFile{Data: []byte("hello"), Mode: 0644},
	}

	watcher := &Watcher{
		fsWatcher:    newNopWatcher(),
		events:       make(chan Event, 100),
		errors:       make(chan error, 10),
		done:         make(chan struct{}),
		pollInterval: time.Millisecond * 150,
		fileSystem:   fileSystem,
		watchDir:     "/home",
		lastScan:     make(map[string]time.Time),
	}

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		eventCh, errCh, err := watchContext(ctx, watcher)
		if err != nil {
			t.Fatalf("watchContext() error = %v", err)
		}

		fmt.Printf("Starting initial scan\n")
		//time.Sleep(time.Millisecond * 10) // wait for initial scan
		fmt.Printf("Initial scan complete\n")
		synctest.Wait()
		fmt.Printf("Modifying file1.txt\n")

		fileSystem["file2.txt"] = &fstest.MapFile{}

		filesReturned := make(map[string]struct{})
	brk:
		for {
			fmt.Printf("Waiting for events...\n")
			select {
			case event, ok := <-eventCh:
				if !ok {
					break brk
				}
				filesReturned[event.Name] = struct{}{}
			case err := <-errCh:
				t.Fatalf("error received: %v", err)
			}
			synctest.Wait()
		}

		if _, ok := filesReturned["/home/file1.txt"]; !ok {
			t.Errorf("expected event for /home/file1.txt, got %v", filesReturned)
		}

		if _, ok := filesReturned["/home/file2.txt"]; !ok {
			t.Errorf("expected event for /home/file2.txt, got %v", filesReturned)
		}

		fmt.Printf("filesReturned: %v\n", filesReturned)
		synctest.Wait()
	})
}

type nopWatcher struct {
	eventCh chan fsnotify.Event
	errCh   chan error
}

func newNopWatcher() *nopWatcher {
	return &nopWatcher{
		eventCh: make(chan fsnotify.Event),
		errCh:   make(chan error),
	}
}

func (n *nopWatcher) Close() error {
	fmt.Printf("Closing nopWatcher\n")
	close(n.eventCh)
	close(n.errCh)
	return nil
}
func (n *nopWatcher) Add(name string) error       { return nil }
func (n *nopWatcher) Events() chan fsnotify.Event { return n.eventCh }
func (n *nopWatcher) Errors() chan error          { return n.errCh }
