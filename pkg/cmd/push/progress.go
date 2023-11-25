package push

import (
	"context"
	"io"
	"os"
	"sync"
	"time"

	"github.com/containerd/console"
	prog "github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/opencontainers/go-digest"
)

type Progress struct {
	display chan *client.SolveStatus
}

type FinishFn func()

// NewProgress creates a new progress writer that simplifies displaying to the console.
// Make sure to run FinishFn to flush remaining logs.  Typically, this is just errors.
func NewProgress(ctx context.Context, progressFmt string) (*Progress, FinishFn, error) {
	// Buffer up to 1024 vertex slices before blocking.
	const channelBufferSize = 1024

	override := os.Getenv("BUILDKIT_PROGRESS")
	if override != "" && progressFmt == prog.PrinterModeAuto {
		progressFmt = override
	}

	w := io.Discard
	var c console.Console
	if progressFmt == prog.PrinterModeAuto || progressFmt == prog.PrinterModeTty {
		w = os.Stderr
		console, err := console.ConsoleFromFile(os.Stderr)
		if err != nil {
			if progressFmt == prog.PrinterModeTty {
				return nil, nil, err
			}
		} else {
			c = console
		}
	}

	progress := &Progress{
		display: make(chan *client.SolveStatus, channelBufferSize),
	}

	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		_, _ = progressui.DisplaySolveStatus(ctx, "Depot Push", c, w, progress.display)
		wg.Done()
	}()

	finish := func() {
		close(progress.display)
		wg.Wait()
	}

	return progress, finish, nil
}

// FinishLogFunc is a function that should be called when a log is finished.
type FinishLogFunc func(err error)

// StartLog starts a log detail span and returns a function that should be called when the log detail is finished.
type StartLogDetailFunc func(message string) FinishLogDetailFunc

// StartLog starts a log span and returns a function that should be called when the log is finished.
func (p *Progress) StartLog(message string) (StartLogDetailFunc, FinishLogFunc) {
	dgst := digest.FromBytes([]byte(identity.NewID()))
	tm := time.Now()
	p.Write(&client.SolveStatus{
		Vertexes: []*client.Vertex{{
			Digest:  dgst,
			Name:    message,
			Started: &tm,
		}},
	})

	logDetail := func(message string) FinishLogDetailFunc {
		return p.StartLogDetail(dgst, message)
	}

	finishLog := func(err error) {
		tm2 := time.Now()
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		p.Write(&client.SolveStatus{
			Vertexes: []*client.Vertex{{
				Digest:    dgst,
				Name:      message,
				Started:   &tm,
				Completed: &tm2,
				Error:     errMsg,
			}},
		})
	}

	return logDetail, finishLog
}

// FinishLogFunc is a function that should be called when a log details are finished.
type FinishLogDetailFunc func()

func (p *Progress) StartLogDetail(vertexDigest digest.Digest, message string) FinishLogDetailFunc {
	started := time.Now()
	p.Write(&client.SolveStatus{
		Statuses: []*client.VertexStatus{
			{
				ID:      message,
				Vertex:  vertexDigest,
				Started: &started,
			},
		},
	})

	return func() {
		completed := time.Now()
		p.Write(&client.SolveStatus{
			Statuses: []*client.VertexStatus{
				{
					ID:        message,
					Vertex:    vertexDigest,
					Started:   &started,
					Completed: &completed,
				},
			},
		})
	}
}

func (p *Progress) Write(s *client.SolveStatus) {
	select {
	case p.display <- s:
	default:
		// if channel is full skip recording vertex time to prevent blocking the push.
	}
}
