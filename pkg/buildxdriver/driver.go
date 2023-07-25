package buildxdriver

import (
	"context"
	"time"

	"github.com/depot/cli/pkg/builder"
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
	buildkit *builder.Buildkit
}

func (d *Driver) Bootstrap(ctx context.Context, reporter progress.Logger) error {
	token := d.cfg.DriverOpts["token"]
	buildID := d.cfg.DriverOpts["buildID"]
	platform := d.cfg.DriverOpts["platform"]

	builder := builder.NewBuilder(token, buildID, platform)

	message := "[depot] launching " + platform + " builder"

	// Try to acquire builder twice
	var err error
	for i := 0; i < 2; i++ {
		finishLog := StartLog(message, reporter)
		d.buildkit, err = builder.StartBuildkit(ctx)
		finishLog(err)
		if err == nil {
			break
		}
	}

	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	message = "[depot] connecting to " + platform + " builder"
	finishLog := StartLog(message, reporter)
	const (
		RETRIES     int           = 120
		RETRY_AFTER time.Duration = time.Second
	)
	err = d.buildkit.WaitUntilReady(ctx, RETRIES, RETRY_AFTER)
	finishLog(err)

	return err
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
