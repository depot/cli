package progresshelper

import (
	depotapi "github.com/depot/cli/pkg/api"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
)

func NewReportingLogger(w progress.Logger, buildID, token string) progress.Logger {
	depotClient := depotapi.NewBuildClient()
	return func(status *client.SolveStatus) {
		w(status)
		reportToAPI(depotClient, status, buildID, token)
	}
}
