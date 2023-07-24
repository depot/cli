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
	driver.InitConfig
	factory     driver.Factory
	builder     *builder.Builder
	builderInfo *builder.AcquiredBuilder

	client *client.Client

	done chan struct{}
}

func (d *Driver) Bootstrap(ctx context.Context, reporter progress.Logger) error {
	var err error
	d.builderInfo, err = d.builder.Acquire(ctx, reporter)
	if err != nil {
		return errors.Wrap(err, "failed to bootstrap builder")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	err = progress.Wrap("[depot] connecting to "+d.builder.Platform+" builder", reporter, func(sub progress.SubLogger) error {
		for i := 0; i < 120; i++ {
			if d.builderInfo.IsReady(ctx) {
				return nil
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Second):
			}
		}

		return errors.New("timed out connecting to builder")
	})

	if err != nil {
		return err
	}

	return nil
}

func (d *Driver) Info(ctx context.Context) (*driver.Info, error) {
	if d.builderInfo == nil {
		return &driver.Info{Status: driver.Stopped}, nil
	}

	if !d.builderInfo.IsReady(ctx) {
		return &driver.Info{Status: driver.Inactive}, nil
	}

	return &driver.Info{Status: driver.Running}, nil
}

func (d *Driver) Client(ctx context.Context) (*client.Client, error) {
	return d.builderInfo.Client(ctx)
}

// Boilerplate

func (d *Driver) Config() driver.InitConfig {
	return d.InitConfig
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
	go func() {
		d.done <- struct{}{}
	}()
	return nil
}

func (d *Driver) Version(ctx context.Context) (string, error) {
	return "", nil
}
