package progresshelper

import (
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
)

func toStatusResponse(status *client.SolveStatus) *controlapi.StatusResponse {
	vertexes := make([]*controlapi.Vertex, 0, len(status.Vertexes))
	for _, v := range status.Vertexes {
		vertexes = append(vertexes, &controlapi.Vertex{
			Digest:        v.Digest,
			Inputs:        v.Inputs,
			Name:          v.Name,
			Cached:        v.Cached,
			Started:       v.Started,
			Completed:     v.Completed,
			Error:         v.Error,
			ProgressGroup: v.ProgressGroup,
		})
	}

	statuses := make([]*controlapi.VertexStatus, 0, len(status.Statuses))
	for _, s := range status.Statuses {
		statuses = append(statuses, &controlapi.VertexStatus{
			ID:        s.ID,
			Vertex:    s.Vertex,
			Name:      s.Name,
			Current:   s.Current,
			Total:     s.Total,
			Timestamp: s.Timestamp,
			Started:   s.Started,
			Completed: s.Completed,
		})
	}

	logs := make([]*controlapi.VertexLog, 0, len(status.Logs))
	for _, l := range status.Logs {
		logs = append(logs, &controlapi.VertexLog{
			Vertex:    l.Vertex,
			Timestamp: l.Timestamp,
			Stream:    int64(l.Stream),
			Msg:       l.Data,
		})
	}

	warnings := make([]*controlapi.VertexWarning, 0, len(status.Warnings))
	for _, w := range status.Warnings {
		warnings = append(warnings, &controlapi.VertexWarning{
			Vertex: w.Vertex,
			Level:  int64(w.Level),
			Short:  w.Short,
			Detail: w.Detail,
			Url:    w.URL,
			Info:   w.SourceInfo,
			Ranges: w.Range,
		})
	}

	return &controlapi.StatusResponse{
		Vertexes: vertexes,
		Statuses: statuses,
		Logs:     logs,
		Warnings: warnings,
	}
}
