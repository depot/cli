package buildxdriver

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/depot/cli/pkg/debuglog"
	"github.com/depot/cli/pkg/machine"
	"github.com/depot/cli/pkg/progresshelper"
	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	dockerclient "github.com/moby/moby/client"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

var (
	_ driver.Driver               = (*Driver)(nil)
	_ driver.BuildPreparer        = (*Driver)(nil)
	_ driver.UncachedClientDriver = (*Driver)(nil)
)

// Driver connects buildx to one Depot builder machine.
type Driver struct {
	cfg         driver.InitConfig
	factory     driver.Factory
	buildID     string
	token       string
	platform    string
	interceptor *interceptor

	mu      sync.Mutex
	machine *machine.Machine
}

func (d *Driver) Bootstrap(ctx context.Context, reporter progress.Logger) error {
	debuglog.Log("Driver Bootstrap() called")
	defer debuglog.Log("Driver Bootstrap() done")

	reportingLogger := progresshelper.NewReporterFromLogger(ctx, reporter, d.buildID, d.token)
	defer reportingLogger.Close()

	message := "[depot] launching " + d.platform + " machine"

	var (
		m   *machine.Machine
		err error
	)
	for range 2 {
		finishLog := progresshelper.StartLog(reportingLogger, message)
		m, err = machine.Acquire(ctx, d.buildID, d.token, d.platform)
		finishLog(err)
		if err == nil {
			break
		}
	}
	if err != nil {
		return err
	}
	m.ClientOptions = d.interceptor.clientOptions()

	d.mu.Lock()
	d.machine = m
	d.mu.Unlock()

	finishLog := progresshelper.StartLog(reportingLogger, "[depot] connecting to "+d.platform+" machine")
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, err = m.Connect(ctx)
	finishLog(err)
	return err
}

func (d *Driver) currentMachine() *machine.Machine {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.machine
}

func (d *Driver) Info(ctx context.Context) (*driver.Info, error) {
	m := d.currentMachine()
	if m == nil {
		return &driver.Info{Status: driver.Stopped}, nil
	}
	if _, err := m.CheckReady(ctx); err != nil {
		return &driver.Info{Status: driver.Inactive}, nil
	}
	return &driver.Info{Status: driver.Running}, nil
}

func (d *Driver) Client(ctx context.Context, _ ...client.ClientOpt) (*client.Client, error) {
	m := d.currentMachine()
	if m == nil {
		return nil, driver.ErrNotRunning{}
	}
	return m.Client(ctx)
}

func (d *Driver) PrepareBuild(_ context.Context, opts *driver.PrepareBuildOptions) error {
	// Depot builders do not keep build history records.
	opts.SolveOpt.Internal = true
	return nil
}

// RequiresUncachedClient makes buildx give each target its own session
// instead of one session for all targets with the same context directory,
// so that the interceptor can give each session the shared key of its target.
// The machine keeps one client, so buildx still uses one connection.
func (d *Driver) RequiresUncachedClient() bool { return true }

func (d *Driver) Features(context.Context) (map[driver.Feature]bool, error) {
	return map[driver.Feature]bool{
		driver.OCIExporter:    true,
		driver.DockerExporter: true,
		driver.CacheExport:    true,
		driver.MultiPlatform:  true,
	}, nil
}

func (d *Driver) Dial(context.Context) (net.Conn, error) {
	return nil, errors.New("the depot driver does not support direct connections")
}

func (d *Driver) HostGatewayIP(context.Context) (net.IP, error) {
	return nil, errors.New("the depot driver does not support host-gateway")
}

func (d *Driver) Version(context.Context) (string, error) { return "", nil }

func (d *Driver) Stop(context.Context, bool) error { return nil }

func (d *Driver) Rm(context.Context, bool, bool, bool) error { return nil }

func (d *Driver) IsMobyDriver() bool { return false }

func (d *Driver) Config() driver.InitConfig { return d.cfg }

func (d *Driver) Factory() driver.Factory { return d.factory }

// Platform returns the machine architecture, "amd64" or "arm64".
func (d *Driver) Platform() string { return d.platform }

// Machine returns the connected machine, or nil before the driver starts.
func (d *Driver) Machine() *machine.Machine { return d.currentMachine() }

// Conn returns a gRPC connection to the machine for services that the
// buildkit client does not expose.
func (d *Driver) Conn(ctx context.Context) (*grpc.ClientConn, error) {
	m := d.currentMachine()
	if m == nil {
		return nil, driver.ErrNotRunning{}
	}
	return m.Conn(ctx)
}

// SolveResponse returns the exporter response that buildkitd returned for
// the solve request with the given reference.
func (d *Driver) SolveResponse(ref string) (*client.SolveResponse, bool) {
	return d.interceptor.response(ref)
}

type factory struct{}

func (*factory) Name() string { return "depot" }

func (*factory) Usage() string { return "depot" }

func (*factory) Priority(context.Context, string, dockerclient.APIClient, map[string][]string) int {
	return 0
}

func (*factory) AllowsInstances() bool { return true }

func (*factory) New(context.Context, driver.InitConfig) (driver.Driver, error) {
	return nil, errors.New("depot drivers are created by buildxdriver.Nodes")
}
