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

type AcquiredBuilder struct {
	Version     string
	Addr        string
	AccessToken string
	CACert      string
	Cert        string
	Key         string
}

func (b *Builder) Acquire(l progress.Logger) (*AcquiredBuilder, error) {
	var resp *api.BuilderResponse
	var err error
	var builder AcquiredBuilder

	acquireFn := func(sub progress.SubLogger) error {
		resp, err = b.depot.GetBuilder(b.BuildID, b.Platform)
		if err != nil {
			return err
		}

		if resp.OK {
			builder.AccessToken = resp.AccessToken
			builder.CACert = resp.CACert
			builder.Cert = resp.Cert
			builder.Key = resp.Key
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

			if resp != nil {
				time.Sleep(time.Duration(resp.PollSeconds) * time.Second)
			} else {
				time.Sleep(time.Duration(1) * time.Second)
			}

			resp, err = b.depot.GetBuilder(b.BuildID, b.Platform)
			if err != nil {
				sub.Log(2, []byte(err.Error()+"\n"))
			}
			count += 1
			if count > 30 {
				return errors.New("Unable to acquire builder connection")
			}
		}

		return nil
	}

	// Try to acquire builder twice
	err = progress.Wrap("[depot] launching "+b.Platform+" builder", l, acquireFn)
	if err != nil {
		err = progress.Wrap("[depot] launching "+b.Platform+" builder", l, acquireFn)
		if err != nil {
			return nil, err
		}
	}

	err = progress.Wrap("[depot] connecting to "+b.Platform+" builder", l, func(sub progress.SubLogger) error {
		proxy, err := newProxyServer(resp.Endpoint, builder.AccessToken)
		if err != nil {
			return errors.Wrap(err, "failed to construct proxy server")
		}

		b.proxy = proxy
		proxy.Start()
		builder.Addr = proxy.Addr().String()

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
	return &builder, err
}
