package api

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"github.com/bufbuild/connect-go"
	"github.com/depot/cli/internal/build"
	"github.com/depot/cli/pkg/ci"
)

var (
	// execution is the execution environment of the CLI, either "terminal" or "ci".
	agent      string
	checkForCI sync.Once
)

func Agent() string {
	checkForCI.Do(func() {
		execution := "terminal"
		if _, isCI := ci.Provider(); isCI {
			execution = "ci"
		}

		agent = fmt.Sprintf("depot-cli/%s/%s/%s/%s", build.Version, runtime.GOOS, runtime.GOARCH, execution)
	})

	return agent
}

func WithUserAgent() connect.ClientOption {
	return connect.WithInterceptors(&agentInterceptor{Agent()})
}

type agentInterceptor struct {
	agent string
}

func (i *agentInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("User-Agent", i.agent)
		return next(ctx, req)
	}
}

func (i *agentInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("User-Agent", i.agent)
		return conn
	}
}

func (i *agentInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
