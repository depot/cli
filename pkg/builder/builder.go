package builder

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/depot/cli/pkg/api"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
)

type Builder struct {
	depot *api.Depot
	proxy *proxyServer

	BuildID  string
	Platform string
}

func NewBuilder(depot *api.Depot, buildID, platform string) *Builder {
	return &Builder{
		depot:    depot,
		BuildID:  buildID,
		Platform: platform,
	}
}

func (b *Builder) Acquire(l progress.Logger) (string, error) {
	var addr string
	var resp *api.BuilderResponse
	var err error
	var accessToken string

	err = progress.Wrap("[depot] launching "+b.Platform+" builder", l, func(sub progress.SubLogger) error {
		resp, err = b.depot.GetBuilder(b.BuildID, b.Platform)
		if err != nil {
			return err
		}

		if resp.OK {
			accessToken = resp.AccessToken
		}

		// Loop if the builder is not ready
		count := 0
		for {
			if resp != nil && resp.OK && resp.BuilderState == "ready" {
				break
			}

			if count > 0 && count%10 == 0 {
				sub.Log(2, []byte("Still waiting for builder to start...\n"))
			}

			time.Sleep(time.Duration(resp.PollSeconds) * time.Second)
			resp, err = b.depot.GetBuilder(b.BuildID, b.Platform)
			count += 1
			if count > 60 {
				return errors.New("Unable to acquire builder connection")
			}
		}

		return nil
	})
	if err != nil {
		return "", err
	}

	err = progress.Wrap("[depot] connecting to "+b.Platform+" builder", l, func(sub progress.SubLogger) error {
		proxy, err := newProxyServer(resp.Endpoint, accessToken)
		if err != nil {
			return errors.Wrap(err, "failed to construct proxy server")
		}

		b.proxy = proxy
		proxy.Start()
		addr = proxy.Addr().String()

		sub.Log(2, []byte("Waiting for builder to report ready...\n"))

		count := 0

		for {
			if count > 30 {
				return fmt.Errorf("timed out waiting for builder to be ready")
			}

			if count > 0 && count%10 == 0 {
				sub.Log(2, []byte("Still waiting for builder to report ready...\n"))
			}

			if count > 0 {
				time.Sleep(time.Second)
			}

			count++

			conn, err := net.Dial("tcp", proxy.Addr().String())
			if err != nil {
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			testClient, err := client.New(ctx, "", client.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return conn, nil
			}))
			if err != nil {
				continue
			}

			ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel2()
			workers, err := testClient.ListWorkers(ctx2)
			if err != nil {
				continue
			}

			if len(workers) > 0 {
				return nil
			}
		}
	})
	return addr, err
}
