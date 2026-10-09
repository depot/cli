package pull

import (
	"context"
	"io"
	"os"
	"sync"

	"github.com/containerd/console"
	"github.com/docker/buildx/util/logutil"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// Specialized printer as the default buildkit one has a hard-coded display phrase, "Building.""
type Printer struct {
	status       chan *client.SolveStatus
	done         <-chan struct{}
	err          error
	warnings     []client.VertexWarning
	logMu        sync.Mutex
	logSourceMap map[digest.Digest]any
}

func (p *Printer) Wait() error                             { close(p.status); <-p.done; return p.err }
func (p *Printer) Write(s *client.SolveStatus)             { p.status <- s }
func (p *Printer) Warnings() []client.VertexWarning        { return p.warnings }
func (p *Printer) WriteBuildRef(target string, ref string) {}

func (p *Printer) ValidateLogSource(dgst digest.Digest, v any) bool {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	src, ok := p.logSourceMap[dgst]
	if ok {
		if src == v {
			return true
		}
	} else {
		p.logSourceMap[dgst] = v
		return true
	}
	return false
}

func (p *Printer) ClearLogSource(v any) {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	for d := range p.logSourceMap {
		if p.logSourceMap[d] == v {
			delete(p.logSourceMap, d)
		}
	}
}

func NewPrinter(ctx context.Context, displayPhrase string, w io.Writer, out console.File, mode string) (*Printer, error) {
	statusCh := make(chan *client.SolveStatus)
	doneCh := make(chan struct{})

	pw := &Printer{
		status:       statusCh,
		done:         doneCh,
		logSourceMap: map[digest.Digest]any{},
	}

	if v := os.Getenv("BUILDKIT_PROGRESS"); v != "" && mode == string(progressui.AutoMode) {
		mode = v
	}

	displayMode := progressui.PlainMode
	displayOut := w
	switch mode {
	case string(progressui.QuietMode):
		displayMode = progressui.QuietMode
	case string(progressui.AutoMode), string(progressui.TtyMode):
		if _, err := console.ConsoleFromFile(out); err == nil {
			displayMode = progressui.TtyMode
			displayOut = out
		} else if mode == string(progressui.TtyMode) {
			return nil, errors.Wrap(err, "failed to get console")
		}
	}

	display, err := progressui.NewDisplay(displayOut, displayMode, progressui.WithPhase(displayPhrase))
	if err != nil {
		return nil, err
	}

	go func() {
		resumeLogs := logutil.Pause(logrus.StandardLogger())
		// not using shared context to not disrupt display but let is finish reporting errors
		// DEPOT: allowed displayPhrase to be overridden.
		pw.warnings, pw.err = display.UpdateFrom(ctx, statusCh)
		resumeLogs()
		close(doneCh)
	}()

	return pw, nil
}
