package progresshelper

import (
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func toStatusResponse(status *client.SolveStatus) *controlapi.StatusResponse {
	vertexes := make([]*controlapi.Vertex, 0, len(status.Vertexes))
	for _, v := range status.Vertexes {
		vertexes = append(vertexes, &controlapi.Vertex{
			Digest:        string(v.Digest),
			Inputs:        digestStrings(v.Inputs),
			Name:          v.Name,
			Cached:        v.Cached,
			Started:       timestamp(v.Started),
			Completed:     timestamp(v.Completed),
			Error:         v.Error,
			ProgressGroup: v.ProgressGroup,
		})
	}

	statuses := make([]*controlapi.VertexStatus, 0, len(status.Statuses))
	for _, s := range status.Statuses {
		statuses = append(statuses, &controlapi.VertexStatus{
			ID:        s.ID,
			Vertex:    string(s.Vertex),
			Name:      s.Name,
			Current:   s.Current,
			Total:     s.Total,
			Timestamp: timestamppb.New(s.Timestamp),
			Started:   timestamp(s.Started),
			Completed: timestamp(s.Completed),
		})
	}

	logs := make([]*controlapi.VertexLog, 0, len(status.Logs))
	for _, l := range status.Logs {
		logs = append(logs, &controlapi.VertexLog{
			Vertex:    string(l.Vertex),
			Timestamp: timestamppb.New(l.Timestamp),
			Stream:    int64(l.Stream),
			Msg:       l.Data,
		})
	}

	warnings := make([]*controlapi.VertexWarning, 0, len(status.Warnings))
	for _, w := range status.Warnings {
		warnings = append(warnings, &controlapi.VertexWarning{
			Vertex: string(w.Vertex),
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

func timestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func digestStrings(digests []digest.Digest) []string {
	if digests == nil {
		return nil
	}
	out := make([]string, len(digests))
	for i, d := range digests {
		out[i] = string(d)
	}
	return out
}
