package buildxdriver

import (
	"context"
	"time"

	"github.com/depot/cli/pkg/builder"
	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
)

var _ driver.Driver = (*Driver)(nil)

type Driver struct {
	config   driver.InitConfig
	factory  driver.Factory
	builder  *builder.Builder
	buildkit *builder.Buildkit
}

func (d *Driver) Bootstrap(ctx context.Context, reporter progress.Logger) error {
	var err error
	d.buildkit, err = d.builder.StartBuildkit(ctx, reporter)
	if err != nil {
		return errors.Wrap(err, "failed to bootstrap builder")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	err = progress.Wrap("[depot] connecting to "+d.builder.Platform+" builder", reporter, func(_ progress.SubLogger) error {
		const (
			RETRIES     int           = 120
			RETRY_AFTER time.Duration = time.Second
		)
		return d.buildkit.WaitUntilReady(ctx, RETRIES, RETRY_AFTER)
	})

	if err != nil {
		return err
	}

	return nil
}

func (d *Driver) Info(ctx context.Context) (*driver.Info, error) {
	if d.buildkit == nil {
		return &driver.Info{Status: driver.Stopped}, nil
	}

	if !d.buildkit.IsReady(ctx) {
		return &driver.Info{Status: driver.Inactive}, nil
	}

	return &driver.Info{Status: driver.Running}, nil
}

func (d *Driver) Client(ctx context.Context) (*client.Client, error) {
	return d.buildkit.Client(ctx)
}

// Boilerplate

func (d *Driver) Config() driver.InitConfig {
	return d.config
}

func (d *Driver) Factory() driver.Factory {
	return d.factory
}

func (d *Driver) Features() map[driver.Feature]bool {
	return map[driver.Feature]bool{
		driver.OCIExporter:    true,
		driver.DockerExporter: true,
		driver.CacheExport:    true,
		driver.MultiPlatform:  true,
	}
}

func (d *Driver) IsMobyDriver() bool {
	return false
}

func (d *Driver) Rm(ctx context.Context, force bool, rmVolume bool, rmDaemon bool) error {
	return nil
}

func (d *Driver) Stop(ctx context.Context, force bool) error {
	return nil
}

func (d *Driver) Version(ctx context.Context) (string, error) {
	return "", nil
}
