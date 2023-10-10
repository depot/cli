package builder

import (
	"context"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"

	depotbuild "github.com/depot/cli/pkg/build"
	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/store"
	"github.com/docker/buildx/store/storeutil"
	"github.com/docker/buildx/util/dockerutil"
	"github.com/docker/buildx/util/imagetools"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/cli/cli/command"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

// Builder represents an active builder object
type Builder struct {
	*store.NodeGroup
	driverFactory driverFactory
	nodes         []Node
	opts          builderOpts
	err           error

	// Depot fields
	token         string
	buildID       string
	buildPlatform string
}

type builderOpts struct {
	dockerCli       command.Cli
	name            string
	txn             *store.Txn
	contextPathHash string
	validate        bool
}

// Option provides a variadic option for configuring the builder.
type Option func(b *Builder)

// WithName sets builder name.
func WithName(name string) Option {
	return func(b *Builder) {
		b.opts.name = name
	}
}

// WithStore sets a store instance used at init.
func WithStore(txn *store.Txn) Option {
	return func(b *Builder) {
		b.opts.txn = txn
	}
}

// WithContextPathHash is used for determining pods in k8s driver instance.
func WithContextPathHash(contextPathHash string) Option {
	return func(b *Builder) {
		b.opts.contextPathHash = contextPathHash
	}
}

// WithSkippedValidation skips builder context validation.
func WithSkippedValidation() Option {
	return func(b *Builder) {
		b.opts.validate = false
	}
}

func WithDepotOptions(buildPlatform string, build depotbuild.Build) Option {
	return func(b *Builder) {
		b.token = build.Token
		b.buildID = build.ID
		b.buildPlatform = buildPlatform
	}
}

// New initializes a new builder client
func New(dockerCli command.Cli, opts ...Option) (_ *Builder, err error) {
	b := &Builder{
		opts: builderOpts{
			dockerCli: dockerCli,
		},
	}
	for _, opt := range opts {
		opt(b)
	}

	if b.opts.txn == nil {
		// if store instance is nil we create a short-lived one using the
		// default store and ensure we release it on completion
		var release func()
		b.opts.txn, release, err = storeutil.GetStore(dockerCli)
		if err != nil {
			return nil, err
		}
		defer release()
	}

	currentContext := dockerCli.CurrentContext()

	amdNode := store.Node{
		Name: "buildx_buildkit_depot_amd64",
		Platforms: []v1.Platform{
			{OS: "linux", Architecture: "amd64"},
			{OS: "linux", Architecture: "amd64", Variant: "v2"},
			{OS: "linux", Architecture: "amd64", Variant: "v3"},
			{OS: "linux", Architecture: "386"},
		},
		DriverOpts: map[string]string{"token": b.token, "platform": "amd64", "buildID": b.buildID},
	}

	armNode := store.Node{
		Name: "buildx_buildkit_depot_arm64",
		Platforms: []v1.Platform{
			{OS: "linux", Architecture: "arm64"},
			{OS: "linux", Architecture: "arm", Variant: "v7"},
			{OS: "linux", Architecture: "arm", Variant: "v6"},
		},
		DriverOpts: map[string]string{"token": b.token, "platform": "arm64", "buildID": b.buildID},
	}

	b.NodeGroup = &store.NodeGroup{
		Name:          currentContext,
		Driver:        "depot",
		Nodes:         []store.Node{},
		DockerContext: true,
	}

	if b.buildPlatform == "linux/amd64" {
		b.NodeGroup.Nodes = []store.Node{amdNode}
	} else if b.buildPlatform == "linux/arm64" {
		b.NodeGroup.Nodes = []store.Node{armNode}
	} else if strings.HasPrefix(runtime.GOARCH, "arm") {
		b.NodeGroup.Nodes = []store.Node{armNode, amdNode}
	} else {
		b.NodeGroup.Nodes = []store.Node{amdNode, armNode}
	}

	return b, nil
}

// Validate validates builder context
func (b *Builder) Validate() error {
	return nil
}

// ContextName returns builder context name if available.
func (b *Builder) ContextName() string {
	return ""
}

// ImageOpt returns registry auth configuration
func (b *Builder) ImageOpt() (imagetools.Opt, error) {
	return storeutil.GetImageConfig(b.opts.dockerCli, b.NodeGroup)
}

// Boot bootstrap a builder
func (b *Builder) Boot(ctx context.Context) (bool, error) {
	toBoot := make([]int, 0, len(b.nodes))
	for idx, d := range b.nodes {
		if d.Err != nil || d.Driver == nil || d.DriverInfo == nil {
			continue
		}
		if d.DriverInfo.Status != driver.Running {
			toBoot = append(toBoot, idx)
		}
	}
	if len(toBoot) == 0 {
		return false, nil
	}

	printer, err := progress.NewPrinter(context.TODO(), os.Stderr, os.Stderr, progress.PrinterModeAuto)
	if err != nil {
		return false, err
	}

	baseCtx := ctx
	eg, _ := errgroup.WithContext(ctx)
	for _, idx := range toBoot {
		func(idx int) {
			eg.Go(func() error {
				pw := progress.WithPrefix(printer, b.NodeGroup.Nodes[idx].Name, len(toBoot) > 1)
				_, err := driver.Boot(ctx, baseCtx, b.nodes[idx].Driver, pw)
				if err != nil {
					b.nodes[idx].Err = err
				}
				return nil
			})
		}(idx)
	}

	err = eg.Wait()
	err1 := printer.Wait()
	if err == nil {
		err = err1
	}

	return true, err
}

// Inactive checks if all nodes are inactive for this builder.
func (b *Builder) Inactive() bool {
	for _, d := range b.nodes {
		if d.DriverInfo != nil && d.DriverInfo.Status == driver.Running {
			return false
		}
	}
	return true
}

// Err returns error if any.
func (b *Builder) Err() error {
	return b.err
}

type driverFactory struct {
	driver.Factory
	once sync.Once
}

// Factory returns the driver factory.
func (b *Builder) Factory(ctx context.Context) (_ driver.Factory, err error) {
	b.driverFactory.once.Do(func() {
		if b.Driver != "" {
			b.driverFactory.Factory, err = driver.GetFactory(b.Driver, true)
			if err != nil {
				return
			}
		} else {
			// empty driver means nodegroup was implicitly created as a default
			// driver for a docker context and allows falling back to a
			// docker-container driver for older daemon that doesn't support
			// buildkit (< 18.06).
			ep := b.NodeGroup.Nodes[0].Endpoint
			var dockerapi *dockerutil.ClientAPI
			dockerapi, err = dockerutil.NewClientAPI(b.opts.dockerCli, b.NodeGroup.Nodes[0].Endpoint)
			if err != nil {
				return
			}
			// check if endpoint is healthy is needed to determine the driver type.
			// if this fails then can't continue with driver selection.
			if _, err = dockerapi.Ping(ctx); err != nil {
				return
			}
			b.driverFactory.Factory, err = driver.GetDefaultFactory(ctx, ep, dockerapi, false)
			if err != nil {
				return
			}
			b.Driver = b.driverFactory.Factory.Name()
		}
	})
	return b.driverFactory.Factory, err
}

// GetBuilders returns all builders
func GetBuilders(dockerCli command.Cli, txn *store.Txn) ([]*Builder, error) {
	storeng, err := txn.List()
	if err != nil {
		return nil, err
	}

	builders := make([]*Builder, len(storeng))
	seen := make(map[string]struct{})
	for i, ng := range storeng {
		b, err := New(dockerCli,
			WithName(ng.Name),
			WithStore(txn),
			WithSkippedValidation(),
		)
		if err != nil {
			return nil, err
		}
		builders[i] = b
		seen[b.NodeGroup.Name] = struct{}{}
	}

	contexts, err := dockerCli.ContextStore().List()
	if err != nil {
		return nil, err
	}
	sort.Slice(contexts, func(i, j int) bool {
		return contexts[i].Name < contexts[j].Name
	})

	for _, c := range contexts {
		// if a context has the same name as an instance from the store, do not
		// add it to the builders list. An instance from the store takes
		// precedence over context builders.
		if _, ok := seen[c.Name]; ok {
			continue
		}
		b, err := New(dockerCli,
			WithName(c.Name),
			WithStore(txn),
			WithSkippedValidation(),
		)
		if err != nil {
			return nil, err
		}
		builders = append(builders, b)
	}

	return builders, nil
}
