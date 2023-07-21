package commands

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/depot/cli/internal/build"
	"github.com/depot/cli/pkg/helpers"
	"github.com/docker/buildx/store"
	"github.com/docker/buildx/store/storeutil"
	"github.com/docker/buildx/util/dockerutil"
	"github.com/docker/cli/cli/command"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	// Register drivers via init() function in factory.go
	_ "github.com/docker/buildx/driver/docker-container"
)

func NewBuildxCmd(dockerCli command.Cli) *cobra.Command {
	var options DepotOptions

	buildx := &cobra.Command{
		Use:   "buildx",
		Short: "Create a depot-powered buildx driver for project",
	}

	use := &cobra.Command{
		Use:   "use",
		Short: "Create and use depot-powered buildx driver for project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuildx(dockerCli, options, args)
		},
	}

	flags := use.Flags()
	depotBuildFlags(use, &options, flags)

	buildx.AddCommand(use)
	return buildx
}

func runBuildx(dockerCli command.Cli, in DepotOptions, args []string) error {
	token := helpers.ResolveToken(context.Background(), in.token)
	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}
	projectName := helpers.ResolveProjectID(in.project)
	if projectName == "" {
		return errors.Errorf("unknown project ID (run `depot init` or use --project or $DEPOT_PROJECT_ID)")
	}

	txn, release, err := storeutil.GetStore(dockerCli)
	if err != nil {
		return fmt.Errorf("unable to get docker store: %w", err)
	}
	defer release()

	if dockerCli.CurrentContext() == "default" && dockerCli.DockerEndpoint().TLSData != nil {
		return fmt.Errorf("could not create a builder instance with TLS data loaded from environment. Please use `docker context create <context-name>` to create a context for current environment and then create a builder instance with `depot buildx use`")
	}
	endpoint, err := dockerutil.GetCurrentEndpoint(dockerCli)
	if err != nil {
		return fmt.Errorf("unable to get current docker endpoint: %w", err)
	}

	nodeName := "depot_" + projectName
	image := "ghcr.io/depot/cli:" + build.Version

	ng := &store.NodeGroup{
		Name:   nodeName,
		Driver: "docker-container",
		Nodes: []store.Node{
			{
				Name:     nodeName + "_amd64",
				Endpoint: endpoint,
				Platforms: []specs.Platform{
					{
						Architecture: "amd64",
						OS:           "linux",
					},
					{
						Architecture: "386",
						OS:           "linux",
					},
				},
				Flags: []string{"buildkitd"},
				DriverOpts: map[string]string{
					"image":                image,
					"env.DEPOT_PROJECT_ID": projectName,
					"env.DEPOT_TOKEN":      token,
					"env.DEPOT_PLATFORM":   "amd64",
				},
			},
			{
				Name:     nodeName + "_arm64",
				Endpoint: endpoint,
				Platforms: []specs.Platform{
					{
						Architecture: "arm64",
						OS:           "linux",
					},
					{
						Architecture: "arm",
						OS:           "linux",
					},
				},
				Flags: []string{"buildkitd"},
				DriverOpts: map[string]string{
					"image":                image,
					"env.DEPOT_PROJECT_ID": projectName,
					"env.DEPOT_TOKEN":      token,
					"env.DEPOT_PLATFORM":   "arm64",
				},
			},
		},
	}

	// Docker uses the first node as default. We try our best to prefer the
	// local machine's architecture.
	if strings.Contains(runtime.GOARCH, "arm") {
		ng.Nodes[0], ng.Nodes[1] = ng.Nodes[1], ng.Nodes[0]
	}

	if err := txn.Save(ng); err != nil {
		return fmt.Errorf("unable to save node group: %w", err)
	}

	global := false
	dflt := false
	if err := txn.SetCurrent(endpoint, nodeName, global, dflt); err != nil {
		return fmt.Errorf("unable to use node group: %w", err)
	}

	fmt.Printf("depot buildx driver %s activated for project %s\n", nodeName, projectName)
	return nil
}
