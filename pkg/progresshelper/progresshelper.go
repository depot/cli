package progresshelper

import (
	"time"

	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/opencontainers/go-digest"
)

// StartLog is a helper to log a message with a progress.Writer.
func StartLog(w progress.Writer, message string) func(err error) {
	dgst := digest.FromBytes([]byte(identity.NewID()))
	tm := time.Now()
	w.Write(&client.SolveStatus{
		Vertexes: []*client.Vertex{{
			Digest:  dgst,
			Name:    message,
			Started: &tm,
		}},
	})

	return func(err error) {
		tm2 := time.Now()
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		w.Write(&client.SolveStatus{
			Vertexes: []*client.Vertex{{
				Digest:    dgst,
				Name:      message,
				Started:   &tm,
				Completed: &tm2,
				Error:     errMsg,
			}},
		})
	}
}

// WithLog wraps a function with timing information.
func WithLog(w progress.Writer, message string, fn func() error) error {
	finishLog := StartLog(w, message)
	err := fn()
	finishLog(err)
	return err
}

// Log writes a log message with no duration.
func Log(w progress.Writer, message string, err error) {
	finishLog := StartLog(w, message)
	finishLog(err)
}
