package fswatch

import (
	"context"
	"os"
	"path/filepath"
	"sort"
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

func (op Op) String() string {
	switch op {
	case Create:
		return "CREATE"
	case Write:
		return "WRITE"
	case Remove:
		return "REMOVE"
	case Rename:
		return "RENAME"
	case Chmod:
		return "CHMOD"
	default:
		return "UNKNOWN"
	}
}

// Watcher watches a directory for file changes using both fsnotify and polling fallback
type Watcher struct {
	fsWatcher    *fsnotify.Watcher
	events       chan Event
	errors       chan error
	done         chan struct{}
	pollInterval time.Duration
	watchDir     string
	lastScan     map[string]time.Time // file path -> last modified time
}

// New creates a new hybrid file watcher
func New(pollInterval time.Duration) (*Watcher, error) {
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &Watcher{
		fsWatcher:    fsWatcher,
		events:       make(chan Event, 100),
		errors:       make(chan error, 10),
		done:         make(chan struct{}),
		pollInterval: pollInterval,
		lastScan:     make(map[string]time.Time),
	}, nil
}

// Add starts watching the specified directory
func (w *Watcher) Add(dir string) error {
	w.watchDir = dir

	// Add to fsnotify watcher
	if err := w.fsWatcher.Add(dir); err != nil {
		return err
	}

	// Start goroutines for both fsnotify and polling
	go w.runFsnotify()
	go w.runPolling()

	return nil
}

// Events returns the event channel
func (w *Watcher) Events() <-chan Event {
	return w.events
}

// Errors returns the error channel
func (w *Watcher) Errors() <-chan error {
	return w.errors
}

// Close stops the watcher and cleans up resources
func (w *Watcher) Close() error {
	close(w.done)
	return w.fsWatcher.Close()
}

// runFsnotify handles fsnotify events
func (w *Watcher) runFsnotify() {
	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.fsWatcher.Events:
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

		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return
			}
			w.errors <- err
		}
	}
}

// runPolling periodically scans the directory for changes
func (w *Watcher) runPolling() {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	// Initial scan
	w.scanDirectory()

	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
			w.scanDirectory()
		}
	}
}

// scanDirectory scans the watch directory and detects changes
func (w *Watcher) scanDirectory() {
	if w.watchDir == "" {
		return
	}

	entries, err := os.ReadDir(w.watchDir)
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
	watcher, err := New(pollInterval)
	if err != nil {
		return nil, nil, err
	}

	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, nil, err
	}

	// Close watcher when context is done
	go func() {
		<-ctx.Done()
		watcher.Close()
	}()

	return watcher.Events(), watcher.Errors(), nil
}
