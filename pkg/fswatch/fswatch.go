package fswatch

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
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

// FSWatcher wraps fsnotify.Watcher so we can test.
type FSWatcher interface {
	io.Closer
	Add(name string) error
	Events() chan fsnotify.Event
	Errors() chan error
}

type fsnotifyWatcher struct {
	w *fsnotify.Watcher
}

func (f *fsnotifyWatcher) Close() error                { return f.w.Close() }
func (f *fsnotifyWatcher) Add(name string) error       { return f.w.Add(name) }
func (f *fsnotifyWatcher) Events() chan fsnotify.Event { return f.w.Events }
func (f *fsnotifyWatcher) Errors() chan error          { return f.w.Errors }

// Watcher watches a directory for file changes using both fsnotify and polling fallback
type Watcher struct {
	fsWatcher    FSWatcher
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
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &Watcher{
		fsWatcher:    &fsnotifyWatcher{w: fsWatcher},
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
	// Add to fsnotify watcher
	if err := w.fsWatcher.Add(w.watchDir); err != nil {
		return err
	}

	// Start goroutines for both fsnotify and polling
	w.wg.Add(2)
	go w.runFsnotify()
	go w.runPolling()

	return nil
}

func (w *Watcher) Events() <-chan Event { return w.events }
func (w *Watcher) Errors() <-chan error { return w.errors }

// Close stops the watcher and cleans up resources.
func (w *Watcher) Close() error {
	fmt.Printf("Closing watcher\n")
	close(w.done)
	err := w.fsWatcher.Close()
	w.wg.Wait()
	close(w.events)
	return err
}

// runFsnotify handles fsnotify events
func (w *Watcher) runFsnotify() {
	defer w.wg.Done()
	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.fsWatcher.Events():
			if !ok {
				return
			}

			// Convert fsnotify event to our event type
			var op Op
			if event.Op&fsnotify.Create != 0 {
				op = Create
			} else if event.Op&fsnotify.Write != 0 {
				op = Write
			} else if event.Op&fsnotify.Remove != 0 {
				op = Remove
			} else if event.Op&fsnotify.Rename != 0 {
				op = Rename
			} else if event.Op&fsnotify.Chmod != 0 {
				op = Chmod
			}

			w.events <- Event{
				Name: event.Name,
				Op:   op,
				Time: time.Now(),
			}

		case err, ok := <-w.fsWatcher.Errors():
			if !ok {
				return
			}
			w.errors <- err
		}
	}
}

// runPolling periodically scans the directory for changes
func (w *Watcher) runPolling() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	// Initial scan
	w.scanDirectory()

	for {
		select {
		case <-w.done:
			fmt.Printf("Stopping polling\n")
			return
		case <-ticker.C:
			w.scanDirectory()
		}
	}
}

// scanDirectory scans the watch directory and detects changes
func (w *Watcher) scanDirectory() {
	entries, err := fs.ReadDir(w.fileSystem, ".")
	if err != nil {
		fmt.Printf("Error reading directory: %v\n", err)
		w.errors <- err
		return
	}

	for _, entry := range entries {
		fmt.Printf("Entry: %s\n", entry.Name())
	}

	currentFiles := make(map[string]time.Time)
	var events []Event

	// Check all current files
	for _, entry := range entries {
		if entry.IsDir() {
			fmt.Printf("Skipping directory: %s\n", entry.Name())
			continue
		}

		filePath := filepath.Join(w.watchDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		modTime := info.ModTime()
		currentFiles[filePath] = modTime

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
	watcher.wg.Add(1)
	go func() {
		<-ctx.Done()
		watcher.wg.Done()
		watcher.Close()
	}()

	return watcher.Events(), watcher.Errors(), nil
}
