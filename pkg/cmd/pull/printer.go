package pull

import (
	"context"
	"io"
	"os"
	"sync"

	"github.com/containerd/console"
	"github.com/docker/buildx/util/logutil"
	prog "github.com/docker/buildx/util/progress"
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
	logSourceMap map[digest.Digest]interface{}
}

func (p *Printer) Wait() error                      { close(p.status); <-p.done; return p.err }
func (p *Printer) Write(s *client.SolveStatus)      { p.status <- s }
func (p *Printer) Warnings() []client.VertexWarning { return p.warnings }

func (p *Printer) ValidateLogSource(dgst digest.Digest, v interface{}) bool {
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

func (p *Printer) ClearLogSource(v interface{}) {
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
		logSourceMap: map[digest.Digest]interface{}{},
	}

	if v := os.Getenv("BUILDKIT_PROGRESS"); v != "" && mode == prog.PrinterModeAuto {
		mode = v
	}

	var c console.Console
	switch mode {
	case prog.PrinterModeQuiet:
		w = io.Discard
	case prog.PrinterModeAuto, prog.PrinterModeTty:
		if cons, err := console.ConsoleFromFile(out); err == nil {
			c = cons
		} else {
			if mode == prog.PrinterModeTty {
				return nil, errors.Wrap(err, "failed to get console")
			}
		}
	}

	go func() {
		resumeLogs := logutil.Pause(logrus.StandardLogger())
		// not using shared context to not disrupt display but let is finish reporting errors
		// DEPOT: allowed displayPhrase to be overridden.
		pw.warnings, pw.err = progressui.DisplaySolveStatus(ctx, displayPhrase, c, w, statusCh)
		resumeLogs()
		close(doneCh)
	}()

	return pw, nil
}
