package buildxdriver

import (
	"context"

	"github.com/depot/cli/pkg/api"
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

func (*factory) Priority(ctx context.Context, api dockerclient.APIClient) int {
	if api == nil {
		return priorityUnsupported
	}
	return prioritySupported
}

func (f *factory) New(ctx context.Context, cfg driver.InitConfig) (driver.Driver, error) {
	project := cfg.DriverOpts["project"]
	token := cfg.DriverOpts["token"]

	depot, err := api.NewDepotFromEnv(token)
	if err != nil {
		return nil, err
	}

	d := &Driver{factory: f, InitConfig: cfg, project: project, token: token, depot: depot}
	return d, nil
}

func (f *factory) AllowsInstances() bool {
	return true
}
