package progresshelper

import (
	"context"
	"errors"
	"sync"
	"time"

	depotapi "github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/debuglog"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	cliv1connect "github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/docker/buildx/util/progress"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
)

var _ progress.Writer = (*Reporter)(nil)

type Reporter struct {
	// Using a function so we can support oth progress.Writer and progress.Logger.
	writer   func(status *client.SolveStatus)
	validate func(digest.Digest, interface{}) bool
	clear    func(interface{})

	buildID string
	token   string
	client  cliv1connect.BuildServiceClient

	ch chan *client.SolveStatus

	closed bool
	mu     sync.Mutex
}

func NewReporter(ctx context.Context, w progress.Writer, buildID, token string) *Reporter {
	r := &Reporter{
		writer:   w.Write,
		validate: w.ValidateLogSource,
		clear:    w.ClearLogSource,
		buildID:  buildID,
		token:    token,
		client:   depotapi.NewBuildClient(),
		ch:       make(chan *client.SolveStatus, 16384),
	}
	go r.Run(ctx)

	return r
}

func NewReporterFromLogger(ctx context.Context, w progress.Logger, buildID, token string) *Reporter {
	r := &Reporter{
		writer:  w,
		buildID: buildID,
		token:   token,
		client:  depotapi.NewBuildClient(),
		ch:      make(chan *client.SolveStatus, 16384),
	}
	go r.Run(ctx)

	return r
}

func (r *Reporter) Write(status *client.SolveStatus) {
	r.writer(status)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}

	select {
	case r.ch <- status:
	default:
	}
}

// Make sure to call Close() after any call to Write.
func (r *Reporter) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()

	close(r.ch)
}

func (r *Reporter) Run(ctx context.Context) {
	sender := r.client.ReportStatusStream(ctx)
	sender.RequestHeader().Add("Authorization", "Bearer "+r.token)
	defer func() {
		_, _ = sender.CloseAndReceive()
	}()

	// Buffer 1 second before sending build timings to the server
	const (
		bufferTimeout = time.Second
	)

	// I'm using a timer here because I may need to retry sending data to the server.
	// With a retry I need to track what data needs to be sent, however, because I
	// may not get more data for a "while" I set it on a timer to force a delivery.
	ticker := time.NewTicker(bufferTimeout)
	defer ticker.Stop()
	statuses := []*controlapi.StatusResponse{}

	for {
		select {
		case status := <-r.ch:
			if status == nil {
				continue
			}

			statuses = append(statuses, toStatusResponse(status))
		case <-ticker.C:
			if len(statuses) == 0 {
				ticker.Reset(bufferTimeout)
				continue
			}

			req := &cliv1.ReportStatusStreamRequest{
				BuildId:  r.buildID,
				Statuses: statuses,
			}

			err := sender.Send(req)
			if err == nil {
				statuses = statuses[:0]
				ticker.Reset(bufferTimeout)
				break
			}
			if errors.Is(err, context.Canceled) {
				// This means we got a context cancel while sending the data.
				// We loop again and will go to the ctx.Done() case.
				continue
			}

			debuglog.Log("unable to send status: %v", err)

			// Reconnect if the connection is broken.
			_, _ = sender.CloseAndReceive()
			sender = r.client.ReportStatusStream(ctx)
			sender.RequestHeader().Add("Authorization", "Bearer "+r.token)
			ticker.Reset(bufferTimeout)
		case <-ctx.Done():
			// Attempt to send any remaining statuses.  This is best effort.  If it fails, we'll just give up.
			for status := range r.ch {
				if status == nil {
					continue
				}
				statuses = append(statuses, toStatusResponse(status))
			}

			if len(statuses) == 0 {
				return
			}

			_, _ = sender.CloseAndReceive()

			// Requires a new context because the previous one was canceled.
			ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			sender = r.client.ReportStatusStream(ctx2)
			sender.RequestHeader().Add("Authorization", "Bearer "+r.token)

			req := &cliv1.ReportStatusStreamRequest{
				BuildId:  r.buildID,
				Statuses: statuses,
			}

			_ = sender.Send(req)
			_, _ = sender.CloseAndReceive()
			cancel()
			return
		}
	}
}

func (r *Reporter) ValidateLogSource(dgst digest.Digest, src interface{}) bool {
	if r.validate == nil {
		return true
	}
	return r.validate(dgst, src)
}

func (r *Reporter) ClearLogSource(src interface{}) {
	if r.clear != nil {
		r.clear(src)
	}
}
