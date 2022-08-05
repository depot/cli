package buildxdriver

import (
	"context"
	"os"
	"time"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/builder"
	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
)

type Driver struct {
	driver.InitConfig
	factory     driver.Factory
	depot       *api.Depot
	builder     *builder.Builder
	builderInfo *builder.AcquiredBuilder
	*tlsOpts
}

type tlsOpts struct {
	serverName string
	caCert     string
	cert       string
	key        string
}

func (d *Driver) Bootstrap(ctx context.Context, l progress.Logger) error {
	builderInfo, err := d.builder.Acquire(l)
	if err != nil {
		return errors.Wrap(err, "failed to bootstrap builder")
	}
	d.builderInfo = builderInfo

	if builderInfo.Cert != "" {
		tls := &tlsOpts{}

		file, err := os.CreateTemp("", "depot-cert")
		if err != nil {
			return errors.Wrap(err, "failed to create temp file")
		}
		defer file.Close()
		err = os.WriteFile(file.Name(), []byte(builderInfo.Cert), 0600)
		if err != nil {
			return errors.Wrap(err, "failed to write cert to temp file")
		}
		tls.cert = file.Name()

		file, err = os.CreateTemp("", "depot-key")
		if err != nil {
			return errors.Wrap(err, "failed to create temp file")
		}
		defer file.Close()
		err = os.WriteFile(file.Name(), []byte(builderInfo.Key), 0600)
		if err != nil {
			return errors.Wrap(err, "failed to write key to temp file")
		}
		tls.key = file.Name()

		file, err = os.CreateTemp("", "depot-ca-cert")
		if err != nil {
			return errors.Wrap(err, "failed to create temp file")
		}
		defer file.Close()
		err = os.WriteFile(file.Name(), []byte(builderInfo.CACert), 0600)
		if err != nil {
			return errors.Wrap(err, "failed to write CA cert to temp file")
		}
		tls.caCert = file.Name()

		d.tlsOpts = tls
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	return progress.Wrap("[depot] connecting to "+d.builder.Platform+" builder", l, func(sub progress.SubLogger) error {
		for i := 0; ; i++ {
			info, err := d.Info(ctx)
			if err != nil {
				return err
			}
			if info.Status != driver.Inactive {
				return nil
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				if i > 10 {
					i = 10
				}
				time.Sleep(time.Duration(i) * time.Second)
			}
		}
	})
}

func (d *Driver) Info(ctx context.Context) (*driver.Info, error) {
	if d.builderInfo == nil {
		return &driver.Info{Status: driver.Stopped}, nil
	}

	c, err := d.Client(ctx)
	if err != nil {
		return &driver.Info{Status: driver.Inactive}, nil
	}

	if _, err := c.ListWorkers(ctx); err != nil {
		return &driver.Info{Status: driver.Inactive}, nil
	}

	return &driver.Info{Status: driver.Running}, nil
}

func (d *Driver) Client(ctx context.Context) (*client.Client, error) {
	opts := []client.ClientOpt{}
	if d.tlsOpts != nil {
		opts = append(opts, client.WithCredentials(d.tlsOpts.serverName, d.tlsOpts.caCert, d.tlsOpts.cert, d.tlsOpts.key))
	}

	return client.New(ctx, d.builderInfo.Addr, opts...)
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
	return nil
}

func (d *Driver) Version(ctx context.Context) (string, error) {
	return "", nil
}
