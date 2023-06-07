package build

import (
	"context"

	"github.com/docker/buildx/util/progress"
)

// DEPOT: Adding a callback(!) to allow processing of the dockerfile.
// Returning an error will stop the build.
//
// Note that the build blocks on this function call.
type DockerfileCallback interface {
	Handle(ctx context.Context, target string, driverIndex int, dockerfile *DockerfileInputs, printer progress.Writer) error
}

type DockerfileHandlers struct {
	handlers []DockerfileCallback
}

func NewDockerfileHandlers(handlers ...DockerfileCallback) *DockerfileHandlers {
	return &DockerfileHandlers{
		handlers: handlers,
	}
}

func (h *DockerfileHandlers) Handle(ctx context.Context, target string, driverIndex int, dockerfile *DockerfileInputs, printer progress.Writer) error {
	for _, handler := range h.handlers {
		if err := handler.Handle(ctx, target, driverIndex, dockerfile, printer); err != nil {
			return err
		}
	}
	return nil
}
