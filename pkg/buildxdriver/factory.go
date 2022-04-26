package buildxdriver

import (
	"context"
	"fmt"

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

func (*factory) Priority(ctx context.Context, api dockerclient.APIClient) int {
	if api == nil {
		return priorityUnsupported
	}
	return prioritySupported
}

func (f *factory) New(ctx context.Context, cfg driver.InitConfig) (driver.Driver, error) {
	platform := cfg.DriverOpts["platform"]
	depot := api.GetContextClient(ctx)
	builders := builder.GetContextBuilders(ctx)
	var builder *builder.Builder
	for _, b := range builders {
		if b.Platform == platform {
			builder = b
			break
		}
	}
	if builder == nil {
		return nil, fmt.Errorf("no builder found for platform %s", platform)
	}

	d := &Driver{factory: f, InitConfig: cfg, addr: "", depot: depot, builder: builder}
	return d, nil
}

func (f *factory) AllowsInstances() bool {
	return true
}
