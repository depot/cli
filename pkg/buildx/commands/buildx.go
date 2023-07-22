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
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	docker "github.com/docker/docker/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	// Register drivers via init() function in factory.go
	_ "github.com/docker/buildx/driver/docker-container"
)

func NewBuildxCmd(dockerCli command.Cli) *cobra.Command {
	var options DepotOptions

	buildx := &cobra.Command{
		Use:   "configure-buildx",
		Short: "Create a depot-powered buildx driver for project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigureBuildx(dockerCli, options, args)
		},
	}

	flags := buildx.Flags()
	depotBuildFlags(&options, flags)

	ls := &cobra.Command{
		Use: "ls",
		RunE: func(cmd *cobra.Command, args []string) error {
			nodes, err := ListDepotNodes(cmd.Context(), dockerCli.Client())
			if err != nil {
				return err
			}
			if len(nodes) == 0 {
				return nil
			}

			fmt.Printf("PROJECT\t\tCONTAINER ID\n")
			for _, node := range nodes {
				fmt.Printf("%s\t%s\n", node.ProjectID, node.ContainerID[:12])
			}
			return nil
		},
	}
	buildx.AddCommand(ls)

	rm := &cobra.Command{
		Use: "rm",
		RunE: func(cmd *cobra.Command, args []string) error {
			nodes, err := ListDepotNodes(cmd.Context(), dockerCli.Client())
			if err != nil {
				return err
			}
			return StopDepotNodes(cmd.Context(), dockerCli.Client(), nodes)
		},
	}
	buildx.AddCommand(rm)

	update := &cobra.Command{
		Use: "update",
		RunE: func(cmd *cobra.Command, args []string) error {
			return UpdateDrivers(cmd.Context(), dockerCli)
		},
	}
	buildx.AddCommand(update)

	return buildx
}

func runConfigureBuildx(dockerCli command.Cli, in DepotOptions, args []string) error {
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

type Node struct {
	ProjectID   string
	ContainerID string
}

func ListDepotNodes(ctx context.Context, client docker.APIClient) ([]Node, error) {
	filters := filters.NewArgs()
	filters.FuzzyMatch("name", "buildx_buildkit_depot_")
	containers, err := client.ContainerList(ctx, types.ContainerListOptions{
		All:     true,
		Filters: filters,
	})
	if err != nil {
		return nil, err
	}

	nodes := []Node{}
	for _, container := range containers {
		for _, name := range container.Names {
			if len(strings.Split(name, "_")) == 5 {
				nodes = append(nodes, Node{
					ProjectID:   strings.Split(name, "_")[3],
					ContainerID: container.ID,
				})
			}
		}
	}

	return nodes, nil
}

func StopDepotNodes(ctx context.Context, client docker.APIClient, nodes []Node) error {
	for _, node := range nodes {
		err := client.ContainerRemove(ctx, node.ContainerID, types.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		if err != nil {
			return err
		}
	}

	return nil
}

func UpdateDrivers(ctx context.Context, dockerCli command.Cli) error {
	nodes, err := ListDepotNodes(ctx, dockerCli.Client())
	if err != nil {
		return err
	}
	err = StopDepotNodes(ctx, dockerCli.Client(), nodes)
	if err != nil {
		return err
	}
	txn, release, err := storeutil.GetStore(dockerCli)
	if err != nil {
		return fmt.Errorf("unable to get docker store: %w", err)
	}
	nodeGroups, err := txn.List()
	if err != nil {
		return fmt.Errorf("unable to list node groups: %w", err)
	}

	for _, nodeGroup := range nodeGroups {
		var save bool
		for i, node := range nodeGroup.Nodes {
			image := node.DriverOpts["image"]
			if strings.HasPrefix(image, "ghcr.io/depot/cli") {
				nodeGroup.Nodes[i].DriverOpts["image"] = "ghcr.io/depot/cli:" + build.Version
				save = true
			}
		}

		if save {
			if err := txn.Save(nodeGroup); err != nil {
				return fmt.Errorf("unable to save node group: %w", err)
			}
		}
	}

	defer release()
	return nil
}
