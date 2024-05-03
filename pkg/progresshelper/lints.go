package progresshelper

import (
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
)

// WriteLint specializes the write to remove the error from the vertex before printing to the terminal.
// We do this because buildx prints error _and_ status for each vertex.  The error
// and status contain the same information, so we remove the error to avoid duplicates.
func WriteLint(w progress.Writer, vertex client.Vertex, statuses []*client.VertexStatus, logs []*client.VertexLog) {
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

	w.Write(status)
}
