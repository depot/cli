package progress

import (
	"context"
	"sync"
	"time"

	"github.com/bufbuild/connect-go"
	depotapi "github.com/depot/cli/pkg/api"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	cliv1connect "github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/docker/buildx/util/progress"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/opencontainers/go-digest"
)

var _ progress.Writer = (*Progress)(nil)

type BuildKitProgressWriter interface {
	Write(*client.SolveStatus)
	ValidateLogSource(digest.Digest, interface{}) bool
	ClearLogSource(interface{})
	Warnings() []client.VertexWarning
	Wait() error
}

type Progress struct {
	buildID string
	token   string

	client   cliv1connect.BuildServiceClient
	vertices chan *client.SolveStatus

	lmu       sync.Mutex
	listeners []Listener

	p BuildKitProgressWriter
}

type FinishFn func()
type Listener func(s *client.SolveStatus)

// NewProgress creates a new progress writer that sends build timings to the server.
// Use the ctx to cancel the long running go routine.
// Make sure to run FinishFn to flush remaining build timings to the server _AFTER_ ctx has been canceled.
// NOTE: this means that you need to defer the FinishFn before deferring the cancel.
func NewProgress(ctx context.Context, buildID, token string, p BuildKitProgressWriter) (*Progress, FinishFn, error) {
	// Buffer up to 1024 vertex slices before blocking the build.
	const channelBufferSize = 1024

	progress := &Progress{
		buildID:  buildID,
		token:    token,
		client:   depotapi.NewBuildClient(),
		vertices: make(chan *client.SolveStatus, channelBufferSize),
		p:        p,
	}

	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() { progress.Run(ctx); wg.Done() }()

	return progress, func() { wg.Wait() }, nil
}

// FinishLogFunc is a function that should be called when a log is finished.
// It records duration and success of the log span to sends to Depot for storage.
type FinishLogFunc func(err error)

// StartLog starts a log detail span and returns a function that should be called when the log detail is finished.
type StartLogDetailFunc func(message string) FinishLogDetailFunc

// StartLog starts a log span and returns a function that should be called when the log is finished.
// Once finished, the log span is recorded and sent to Depot for storage.
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
// It records duration the log detail span to sends to Depot for storage.
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

// WithLog wraps a function with timing information.
func (p *Progress) WithLog(message string, fn func() error) error {
	_, finishLog := p.StartLog(message)
	err := fn()
	finishLog(err)
	return err
}

// Log writes a log message with no duration.
func (p *Progress) Log(message string, err error) {
	_, finishLog := p.StartLog(message)
	finishLog(err)
}

func (p *Progress) Write(s *client.SolveStatus) {
	// Only buffer vertices to send if this progress writer is running in the context of an active build
	if p.HasActiveBuild() {
		select {
		case p.vertices <- s:
		default:
			// if channel is full skip recording vertex time to prevent blocking the build.
		}
	}

	p.p.Write(s)

	p.lmu.Lock()
	defer p.lmu.Unlock()
	for _, listener := range p.listeners {
		listener(s)
	}
}

// WriteLint specializes the write to remove the error from the vertex before printing to the terminal.
// We do this because buildx prints error _and_ status for each vertex.  The error
// and status contain the same information, so we remove the error to avoid duplicates.
//
// However, the error message is still uploaded to the API.
func (p *Progress) WriteLint(vertex client.Vertex, statuses []*client.VertexStatus, logs []*client.VertexLog) {
	// We are stripping the error here because the UX printing the error and
	// the status show the same information twice.
	withoutError := vertex
	if withoutError.Error != "" {
		// Filling in a generic error message to cause the UX to fail with a red color.
		withoutError.Error = "linting failed "
	}

	status := &client.SolveStatus{
		Vertexes: []*client.Vertex{&withoutError},
		Statuses: statuses,
		Logs:     logs,
	}
	// Only buffer vertices to send if this progress writer is running in the context of an active build
	if p.HasActiveBuild() {
		select {
		case p.vertices <- status:
		default:
			// if channel is full skip recording vertex time to prevent blocking the build.
		}
	}

	p.p.Write(status)
}

func (p *Progress) ValidateLogSource(digest digest.Digest, v interface{}) bool {
	return p.p.ValidateLogSource(digest, v)
}

func (p *Progress) ClearLogSource(v interface{}) {
	p.p.ClearLogSource(v)
}

func (p *Progress) Wait() error {
	return p.p.Wait()
}

func (p *Progress) Warnings() []client.VertexWarning {
	return p.p.Warnings()
}

// Run should be started in a go routine to send build timings to the server on a timer.
//
// Cancel the context to stop the go routine.
func (p *Progress) Run(ctx context.Context) {
	// Return if this progress writer isn't running in the context of an active build
	if !p.HasActiveBuild() {
		return
	}

	// Buffer 1 second before sending build timings to the server
	const (
		bufferTimeout = time.Second
	)

	ticker := time.NewTicker(bufferTimeout)
	defer ticker.Stop()

	statuses := []*client.SolveStatus{}

	for {
		select {
		case status := <-p.vertices:
			if status == nil {
				continue
			}
			statuses = append(statuses, status)
		case <-ticker.C:
			// Requires a new context because the previous one may be canceled while we are
			// sending the build timings.  At most one will wait 5 seconds.
			ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := p.ReportStatus(ctx2, statuses); err == nil {
				// Clear all reported steps.
				statuses = statuses[:0]
			}

			ticker.Reset(bufferTimeout)
			cancel()
		case <-ctx.Done():
			// Send all remaining build timings before exiting.
			for {
				select {
				case status := <-p.vertices:
					if status == nil {
						continue
					}

					statuses = append(statuses, status)
				default:
					// Requires a new context because the previous one was canceled.
					ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					// Any errors are ignored and steps are not reported.
					_ = p.ReportStatus(ctx2, statuses)
					cancel()

					return
				}
			}
		}
	}
}

func (p *Progress) HasActiveBuild() bool {
	return p.buildID != "" && p.token != ""
}

func (p *Progress) ReportStatus(ctx context.Context, ss []*client.SolveStatus) error {
	if len(ss) == 0 {
		return nil
	}

	statuses := make([]*controlapi.StatusResponse, 0, len(ss))
	stableDigests := map[string]string{}
	for _, s := range ss {
		status := s.Marshal()
		statuses = append(statuses, status...)

		for _, sr := range status {
			for _, v := range sr.Vertexes {
				// Some vertex may not have stable digests.
				// This generally happens when the vertex in an informational log like "pulling fs layer."
				stableDigest := v.StableDigest.String()
				if stableDigest == "" {
					stableDigest = digest.FromString(v.Name).String()
				}
				stableDigests[v.Digest.String()] = stableDigest
			}
		}
	}

	req := &cliv1.ReportStatusRequest{
		BuildId:       p.buildID,
		Statuses:      statuses,
		StableDigests: stableDigests,
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := p.client.ReportStatus(ctx, depotapi.WithAuthentication(connect.NewRequest(req), p.token))
	if err != nil {
		// No need to log errors to the user as it is fine if we miss some build timings.
		return err
	}

	return nil
}

func (p *Progress) AddListener(l Listener) {
	p.lmu.Lock()
	defer p.lmu.Unlock()
	p.listeners = append(p.listeners, l)
}
