package progress

import (
	"time"

	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/opencontainers/go-digest"
)

// Log is a helper to log a message with a progress.Writer.
// progress.Writer is pretty intricate and thus can be a lot of boilerplate.

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
