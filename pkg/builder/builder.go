package builder

import (
	"time"

	"github.com/depot/cli/pkg/api"
	"github.com/docker/buildx/util/progress"
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

	proxy, err := newProxyServer(resp.Endpoint, builder.AccessToken)
	if err != nil {
		return nil, errors.Wrap(err, "failed to construct proxy server")
	}

	b.proxy = proxy
	proxy.Start()
	builder.Addr = proxy.Addr().String()

	return &builder, err
}
