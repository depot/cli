package buildxdriver

import (
	"context"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/builder"
	"github.com/docker/buildx/driver"
	dockerclient "github.com/docker/docker/client"
)

const prioritySupported = 30
const priorityUnsupported = 70

func init() {
	driver.Register(&factory{})
}

type factory struct {
}

func (*factory) Name() string {
	return "depot"
}

func (*factory) Usage() string {
	return "depot"
}

func (*factory) Priority(ctx context.Context, endpoint string, api dockerclient.APIClient) int {
	if api == nil {
		return priorityUnsupported
	}
	return prioritySupported
}

func (f *factory) New(ctx context.Context, cfg driver.InitConfig) (driver.Driver, error) {
	platform := cfg.DriverOpts["platform"]
	buildID := cfg.DriverOpts["buildID"]
	depot := api.GetContextClient(ctx)
	builder := builder.NewBuilder(depot, buildID, platform)

	d := &Driver{factory: f, InitConfig: cfg, builderInfo: nil, depot: depot, builder: builder, done: make(chan struct{})}
	return d, nil
}

func (f *factory) AllowsInstances() bool {
	return true
}
