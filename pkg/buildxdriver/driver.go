package buildxdriver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/depot/cli/pkg/api"
	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
)

type Driver struct {
	driver.InitConfig
	factory driver.Factory
	depot   *api.Depot
	project string
	token   string
	proxy   *proxyServer
	resp    *api.InitResponse
}

func (d *Driver) IsMobyDriver() bool {
	return false
}

func (d *Driver) Config() driver.InitConfig {
	return d.InitConfig
}

func (d *Driver) Bootstrap(ctx context.Context, l progress.Logger) error {
	return progress.Wrap("[internal] booting depot buildkit", l, func(sub progress.SubLogger) error {
		resp, err := d.depot.InitBuild(d.project)
		if err != nil {
			return err
		}
		d.resp = resp

		proxy, err := newProxyServer(resp.BaseURL, resp.AccessToken, resp.ID)
		if err != nil {
			return errors.Wrap(err, "failed to construct proxy server")
		}

		d.proxy = proxy
		proxy.Start()

		return sub.Wrap("Connecting to builder "+d.Name, func() error {
			err = waitForReady(resp, proxy)
			return err
		})
	})
}

func (d *Driver) Info(ctx context.Context) (*driver.Info, error) {
	if d.proxy == nil {
		return &driver.Info{Status: driver.Stopped}, nil
	} else {
		return &driver.Info{Status: driver.Running}, nil
	}
}

func (d *Driver) Stop(ctx context.Context, force bool) error {
	fmt.Println("STOP CALLED")
	if d.proxy != nil {
		d.proxy.Close()
	}
	if d.resp != nil {
		err := d.depot.FinishBuild(d.resp.ID)
		if err != nil {
			return err
		}
	}

	return nil
}

func (d *Driver) Rm(ctx context.Context, force bool, rmVolume bool) error {
	fmt.Println("RM CALLED")
	return nil
}

func (d *Driver) Client(ctx context.Context) (*client.Client, error) {
	if d.proxy == nil {
		return nil, errors.New("failed to create builder proxy before use")
	}

	conn, err := net.Dial("tcp", d.proxy.Addr().String())
	if err != nil {
		return nil, errors.Wrap(err, "failed to connect to buildkit")
	}

	// exp, err := detect.Exporter()
	// if err != nil {
	// 	return nil, err
	// }

	// td, _ := exp.(client.TracerDelegate)

	return client.New(ctx, "", client.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return conn, nil
	})) // , client.WithTracerDelegate(td))
}

func (d *Driver) Factory() driver.Factory {
	return d.factory
}

func (d *Driver) Features() map[driver.Feature]bool {
	return map[driver.Feature]bool{
		driver.OCIExporter:    true,
		driver.DockerExporter: true,

		driver.CacheExport:   true,
		driver.MultiPlatform: true,
	}
}

func waitForReady(build *api.InitResponse, proxy *proxyServer) error {
	fmt.Fprintf(os.Stderr, "Waiting for connection to BuildKit...\n")
	httpClient := &http.Client{}

	fmt.Println(build.ID)
	fmt.Println(build.AccessToken)

	count := 0

	for {
		req, err := http.NewRequest("GET", fmt.Sprintf("%s/ready-%s/", build.BaseURL, build.ID), nil)
		if err != nil {
			return err
		}
		req.Header.Add("Authorization", fmt.Sprintf("bearer %s", build.AccessToken))

		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		fmt.Fprintf(os.Stderr, "Got status code %d\n", resp.StatusCode)

		if resp.StatusCode == http.StatusOK {
			break
		}

		count++
		if count > 30 {
			return fmt.Errorf("timed out waiting for build to be ready")
		}

		time.Sleep(time.Second)
	}

	fmt.Fprintf(os.Stderr, "Waiting for BuildKit to report ready...\n")

	count = 0

	for {
		if count > 30 {
			return fmt.Errorf("timed out waiting for buildkit to be ready")
		}

		if count > 0 {
			time.Sleep(time.Second)
		}

		count++

		fmt.Println("Checking if buildkit is ready")

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

		fmt.Printf("%+v\n", workers)

		if len(workers) > 0 {
			return nil
		}
	}
}
