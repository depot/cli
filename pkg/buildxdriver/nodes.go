package buildxdriver

import (
	"runtime"
	"strings"

	depotbuild "github.com/depot/cli/pkg/build"
	"github.com/docker/buildx/builder"
	"github.com/docker/buildx/driver"
	"github.com/docker/buildx/store"
	"github.com/docker/buildx/store/storeutil"
	"github.com/docker/buildx/util/dockerutil/dockerconfig"
	"github.com/docker/buildx/util/imagetools"
	"github.com/docker/cli/cli/command"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

const builderName = "depot"

var (
	amd64Platforms = []ocispecs.Platform{
		{OS: "linux", Architecture: "amd64"},
		{OS: "linux", Architecture: "amd64", Variant: "v2"},
		{OS: "linux", Architecture: "amd64", Variant: "v3"},
		{OS: "linux", Architecture: "amd64", Variant: "v4"},
		{OS: "linux", Architecture: "386"},
	}
	arm64Platforms = []ocispecs.Platform{
		{OS: "linux", Architecture: "arm64"},
		{OS: "linux", Architecture: "arm", Variant: "v8"},
		{OS: "linux", Architecture: "arm", Variant: "v7"},
		{OS: "linux", Architecture: "arm", Variant: "v6"},
	}
)

// Nodes returns one buildx node for each Depot machine that the build may
// use. The first node is the default for builds without a platform.
//
// buildPlatform is "linux/amd64" or "linux/arm64" to run every platform on
// one machine, or "dynamic" to choose the machine by platform.
func Nodes(dockerCli command.Cli, build *depotbuild.Build, buildPlatform string) []builder.Node {
	architectures := []string{"amd64", "arm64"}
	switch {
	case buildPlatform == "linux/amd64":
		architectures = []string{"amd64"}
	case buildPlatform == "linux/arm64":
		architectures = []string{"arm64"}
	case strings.HasPrefix(runtime.GOARCH, "arm"):
		architectures = []string{"arm64", "amd64"}
	}

	imageOpt := imagetools.Opt{Auth: build.AuthProvider(dockerconfig.LoadAuthConfig(dockerCli))}
	proxyConfig := storeutil.GetProxyConfig(dockerCli)

	nodes := make([]builder.Node, 0, len(architectures))
	for _, arch := range architectures {
		platforms := amd64Platforms
		if arch == "arm64" {
			platforms = arm64Platforms
		}
		name := "buildx_buildkit_depot_" + arch
		d := &Driver{
			cfg: driver.InitConfig{
				Name:         name,
				ContextStore: dockerCli.ContextStore(),
				Auth:         imageOpt.Auth,
				Platforms:    platforms,
			},
			factory:     &factory{},
			buildID:     build.ID,
			token:       build.Token,
			platform:    arch,
			interceptor: newInterceptor(),
		}
		nodes = append(nodes, builder.Node{
			Node:        store.Node{Name: name, Platforms: platforms},
			Builder:     builderName,
			Driver:      &driver.DriverHandle{Driver: d},
			ImageOpt:    imageOpt,
			ProxyConfig: proxyConfig,
			Platforms:   platforms,
		})
	}
	return nodes
}

// FromNode returns the Depot driver of a node, or nil for other drivers.
func FromNode(node builder.Node) *Driver {
	if node.Driver == nil {
		return nil
	}
	d, _ := node.Driver.Driver.(*Driver)
	return d
}
