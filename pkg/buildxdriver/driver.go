package buildxdriver

import (
	"context"
	"net"
	"os"
	"strings"
	"time"

	"github.com/depot/cli/pkg/builder"
	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
)

type Driver struct {
	driver.InitConfig
	factory     driver.Factory
	builder     *builder.Builder
	builderInfo *builder.AcquiredBuilder
	*tlsOpts

	client *client.Client

	done chan struct{}
}

type tlsOpts struct {
	serverName string
	caCert     string
	cert       string
	key        string
}

func (d *Driver) Bootstrap(ctx context.Context, reporter progress.Logger) error {
	builderInfo, err := d.builder.Acquire(ctx, reporter)
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

	err = progress.Wrap("[depot] connecting to "+d.builder.Platform+" builder", reporter, func(sub progress.SubLogger) error {
		for i := 0; i < 120; i++ {

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
				time.Sleep(1 * time.Second)
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
	if d.client != nil {
		return d.client, nil
	}

	if d.builderInfo == nil {
		return nil, errors.New("builder not started")
	}

	opts := []client.ClientOpt{}
	if d.tlsOpts != nil {
		opts = append(opts, client.WithCredentials(d.tlsOpts.serverName, d.tlsOpts.caCert, d.tlsOpts.cert, d.tlsOpts.key))
	}

	opts = append(opts, client.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		addr = strings.TrimPrefix(addr, "tcp://")
		return net.Dial("tcp", addr)
	}))

	c, err := client.New(ctx, d.builderInfo.Addr, opts...)
	if err != nil {
		return nil, err
	}
	d.client = c
	return c, nil
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
