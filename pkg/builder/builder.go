package builder

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/depot/cli/pkg/api"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
)

type Builder struct {
	buildID string
	depot   *api.Depot
	proxy   *proxyServer
}

func NewBuilder(depot *api.Depot) *Builder {
	return &Builder{
		depot: depot,
	}
}

func (b *Builder) Acquire(l progress.Logger, project string) (string, error) {
	var addr string
	var resp *api.InitResponse
	var err error

	err = progress.Wrap("[depot] provisioning builder builder", l, func(sub progress.SubLogger) error {
		count := 0
		for {
			resp, err = b.depot.InitBuild(project)
			if err != nil {
				return err
			}

			if resp.OK && resp.Busy {
				sub.Log(2, []byte("Builder is busy, waiting for concurrency..."))
				time.Sleep(1 * time.Second)
			} else if resp.OK {
				break
			}

			count += 1
			if count > 30 {
				return errors.New("timeout waiting for builder")
			}
		}

		b.buildID = resp.ID
		return nil
	})
	if err != nil {
		return "", err
	}

	err = progress.Wrap("[depot] connecting to builder "+resp.ID+" in project "+project, l, func(sub progress.SubLogger) error {
		proxy, err := newProxyServer(resp.BaseURL, resp.AccessToken, resp.ID)
		if err != nil {
			return errors.Wrap(err, "failed to construct proxy server")
		}

		b.proxy = proxy
		proxy.Start()
		addr = proxy.Addr().String()

		sub.Log(0, []byte("Waiting for connection to BuildKit "+resp.ID))
		httpClient := &http.Client{}

		count := 0

		for {
			req, err := http.NewRequest("GET", fmt.Sprintf("%s/ready-%s/", resp.BaseURL, resp.ID), nil)
			if err != nil {
				return err
			}
			req.Header.Add("Authorization", fmt.Sprintf("bearer %s", resp.AccessToken))

			resp, err := httpClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				break
			}

			count++
			if count > 30 {
				return fmt.Errorf("timed out waiting for build to be ready")
			}

			time.Sleep(time.Second)
		}

		sub.Log(2, []byte("Waiting for BuildKit to report ready..."))

		count = 0

		for {
			if count > 30 {
				return fmt.Errorf("timed out waiting for buildkit to be ready")
			}

			if count > 0 {
				time.Sleep(time.Second)
			}

			count++

			conn, err := net.Dial("tcp", proxy.Addr().String())
			if err != nil {
				continue
			}

			testClient, err := client.New(context.TODO(), "", client.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return conn, nil
			}))
			if err != nil {
				continue
			}

			workers, err := testClient.ListWorkers(context.TODO())
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

func (b *Builder) Release() error {
	return b.depot.FinishBuild(b.buildID)
}
