package buildxdriver

import (
	"context"
	"fmt"
	"net"

	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
)

type Driver struct {
	driver.InitConfig
	factory driver.Factory
	addr    string
}

func (d *Driver) IsMobyDriver() bool {
	return false
}

func (d *Driver) Config() driver.InitConfig {
	return d.InitConfig
}

func (d *Driver) Bootstrap(ctx context.Context, l progress.Logger) error {
	return nil
}

func (d *Driver) Info(ctx context.Context) (*driver.Info, error) {
	return &driver.Info{Status: driver.Running}, nil
}

func (d *Driver) Stop(ctx context.Context, force bool) error {
	fmt.Println("STOP CALLED")
	return nil
}

func (d *Driver) Rm(ctx context.Context, force bool, rmVolume bool, rmDaemon bool) error {
	fmt.Println("RM CALLED")
	return nil
}

func (d *Driver) Client(ctx context.Context) (*client.Client, error) {
	conn, err := net.Dial("tcp", d.addr)
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
