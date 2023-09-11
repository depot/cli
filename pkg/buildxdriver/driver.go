package buildxdriver

import (
	"context"
	"time"

	"github.com/depot/cli/pkg/machine"
	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/opencontainers/go-digest"
)

var _ driver.Driver = (*Driver)(nil)

type Driver struct {
	cfg driver.InitConfig

	factory  driver.Factory
	buildkit *machine.Machine
}

func (d *Driver) Bootstrap(ctx context.Context, reporter progress.Logger) error {
	buildID := d.cfg.DriverOpts["buildID"]
	token := d.cfg.DriverOpts["token"]
	platform := d.cfg.DriverOpts["platform"]

	message := "[depot] launching " + platform + " machine"

	// Try to acquire machine twice
	var err error
	for i := 0; i < 2; i++ {
		finishLog := StartLog(message, reporter)
		d.buildkit, err = machine.Acquire(ctx, buildID, token, platform)
		finishLog(err)
		if err == nil {
			break
		}
	}

	if err != nil {
		return err
	}

	message = "[depot] connecting to " + platform + " machine"
	finishLog := StartLog(message, reporter)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	_, err = d.buildkit.Connect(ctx)
	finishLog(err)

	// Store the machine connection details in the driver config so they can be
	// accessed by clients that need to create new connections to the machine.
	// This was done because the buildkit client doesn't expose the connection.
	// This was added originally for the registry proxy.
	d.cfg.DriverOpts["addr"] = d.buildkit.Addr
	d.cfg.DriverOpts["serverName"] = d.buildkit.ServerName
	d.cfg.DriverOpts["caCert"] = d.buildkit.CACert
	d.cfg.DriverOpts["key"] = d.buildkit.Key
	d.cfg.DriverOpts["cert"] = d.buildkit.Cert

	return err
}

func (d *Driver) Info(ctx context.Context) (*driver.Info, error) {
	if d.buildkit == nil {
		return &driver.Info{Status: driver.Stopped}, nil
	}

	if _, err := d.buildkit.CheckReady(ctx); err != nil {
		return &driver.Info{Status: driver.Inactive}, nil
	}

	return &driver.Info{Status: driver.Running}, nil
}

func (d *Driver) Client(ctx context.Context) (*client.Client, error) {
	return d.buildkit.Client(ctx)
}

// Boilerplate

func (d *Driver) Config() driver.InitConfig {
	return d.cfg
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

func StartLog(message string, logger progress.Logger) func(err error) {
	dgst := digest.FromBytes([]byte(identity.NewID()))
	tm := time.Now()
	logger(&client.SolveStatus{
		Vertexes: []*client.Vertex{{
			Digest:  dgst,
			Name:    message,
			Started: &tm,
		}},
	})

	return func(err error) {
		tm2 := time.Now()
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		logger(&client.SolveStatus{
			Vertexes: []*client.Vertex{{
				Digest:    dgst,
				Name:      message,
				Started:   &tm,
				Completed: &tm2,
				Error:     errMsg,
			}},
		})
	}
}
