package progresshelper

import (
	depotapi "github.com/depot/cli/pkg/api"
	cliv1connect "github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
)

var _ progress.Writer = (*reportingWriter)(nil)

type reportingWriter struct {
	progress.Writer

	buildID string
	token   string
	client  cliv1connect.BuildServiceClient
}

func NewReportingWriter(w progress.Writer, buildID, token string) progress.Writer {
	return &reportingWriter{
		Writer:  w,
		buildID: buildID,
		token:   token,
		client:  depotapi.NewBuildClient(),
	}
}

func (s *reportingWriter) Write(status *client.SolveStatus) {
	s.Writer.Write(status)
	reportToAPI(s.client, status, s.buildID, s.token)
}
