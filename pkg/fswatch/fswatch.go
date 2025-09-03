package fswatch

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Event represents a file system event
type Event struct {
	Name string    // file path
	Op   Op        // operation that triggered the event
	Time time.Time // when the event occurred
}

// Op describes file system operations
type Op uint32

const (
	Create Op = 1 << iota
	Write
	Remove
	Rename
	Chmod
)

// Watcher watches a directory for file changes using both fsnotify and polling fallback
type Watcher struct {
	events       chan Event
	errors       chan error
	done         chan struct{}
	pollInterval time.Duration
	fileSystem   fs.FS
	watchDir     string
	lastScan     map[string]time.Time // file path -> last modified time
	wg           sync.WaitGroup
}

// New creates a new hybrid file watcher
func New(pollInterval time.Duration, dir string) (*Watcher, error) {
	return &Watcher{
		events:       make(chan Event, 100),
		errors:       make(chan error, 10),
		done:         make(chan struct{}),
		pollInterval: pollInterval,
		fileSystem:   os.DirFS(dir),
		watchDir:     dir,
		lastScan:     make(map[string]time.Time),
	}, nil
}

// Watch starts watching the directory for file changes.
func (w *Watcher) Watch() error {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.runPolling()
	}()

	return nil
}

func (w *Watcher) Events() <-chan Event { return w.events }
func (w *Watcher) Errors() <-chan error { return w.errors }

// Close stops the watcher and cleans up resources.
func (w *Watcher) Close() error {
	close(w.done)
	w.wg.Wait()
	close(w.events)
	return nil
}

// runPolling periodically scans the directory for changes
func (w *Watcher) runPolling() {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	// Initial scan
	w.scanDirectory(true)

	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
			w.scanDirectory(false)
		}
	}
}

// scanDirectory scans the watch directory and detects changes
func (w *Watcher) scanDirectory(initial bool) {
	entries, err := fs.ReadDir(w.fileSystem, ".")
	if err != nil {
		w.errors <- err
		return
	}

	currentFiles := make(map[string]time.Time)
	var events []Event

	// Check all current files
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(w.watchDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		modTime := info.ModTime()
		currentFiles[filePath] = modTime

		if initial {
			w.lastScan[filePath] = modTime
			continue
		}

		if lastModTime, exists := w.lastScan[filePath]; exists {
			// File existed before, check if modified
			if modTime.After(lastModTime) {
				events = append(events, Event{
					Name: filePath,
					Op:   Write,
					Time: modTime,
				})
			}
		} else {
			// New file
			events = append(events, Event{
				Name: filePath,
				Op:   Create,
				Time: modTime,
			})
		}
	}

	// Check for removed files
	for filePath := range w.lastScan {
		if _, exists := currentFiles[filePath]; !exists {
			events = append(events, Event{
				Name: filePath,
				Op:   Remove,
				Time: time.Now(),
			})
		}
	}

	// Update last scan state
	w.lastScan = currentFiles

	// Sort events by timestamp and send them
	sort.Slice(events, func(i, j int) bool {
		return events[i].Time.Before(events[j].Time)
	})

	for _, event := range events {
		select {
		case w.events <- event:
		case <-w.done:
			return
		}
	}
}

// WatchContext watches a directory with context cancellation
func WatchContext(ctx context.Context, dir string, pollInterval time.Duration) (<-chan Event, <-chan error, error) {
	watcher, err := New(pollInterval, dir)
	if err != nil {
		return nil, nil, err
	}
	return watchContext(ctx, watcher)
}

// watchContext is split from WatchContext for testing watchers.
func watchContext(ctx context.Context, watcher *Watcher) (<-chan Event, <-chan error, error) {
	if err := watcher.Watch(); err != nil {
		watcher.Close()
		return nil, nil, err
	}

	// Close watcher when context is done
	go func() {
		select {
		case <-ctx.Done():
		case <-watcher.done:
		}
		watcher.Close()
	}()

	return watcher.Events(), watcher.Errors(), nil
}
