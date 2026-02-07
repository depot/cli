package sandbox

import (
	"fmt"
	"io"
	"sync"
	"time"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type spinner struct {
	mu      sync.Mutex
	message string
	started bool
	stopped bool
	stop    chan struct{}
	done    chan struct{}
	w       io.Writer
}

func newSpinner(message string, w io.Writer) *spinner {
	return &spinner{
		message: message,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		w:       w,
	}
}

func (s *spinner) Start() {
	s.started = true
	go func() {
		defer close(s.done)
		i := 0
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				fmt.Fprintf(s.w, "\r\033[K")
				return
			case <-ticker.C:
				s.mu.Lock()
				msg := s.message
				s.mu.Unlock()
				fmt.Fprintf(s.w, "\r\033[K%s %s", spinnerFrames[i%len(spinnerFrames)], msg)
				i++
			}
		}
	}()
}

func (s *spinner) Update(message string) {
	s.mu.Lock()
	s.message = message
	s.mu.Unlock()
}

func (s *spinner) Stop() {
	if !s.started {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.mu.Unlock()
	close(s.stop)
	<-s.done
}
