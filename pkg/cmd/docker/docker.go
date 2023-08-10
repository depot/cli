package docker

import (
	"context"
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"

	"github.com/depot/cli/internal/build"
	"github.com/depot/cli/pkg/helpers"
	"github.com/docker/buildx/store"
	"github.com/docker/buildx/store/storeutil"
	"github.com/docker/buildx/util/dockerutil"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/config"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	dockerclient "github.com/docker/docker/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func NewCmdConfigureDocker(dockerCli command.Cli) *cobra.Command {
	uninstall := false
	var (
		project string
		token   string
	)

	cmd := &cobra.Command{
		Use:   "configure-docker",
		Short: "Configure Docker to use Depot for builds",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := config.Dir()
			if err := os.MkdirAll(dir, 0755); err != nil {
				return errors.Wrap(err, "could not create docker config")
			}

			if uninstall {
				err := uninstallDepotPlugin(dir)
				if err != nil {
					return errors.Wrap(err, "could not uninstall depot plugin")
				}

				err = RemoveDrivers(cmd.Context(), dockerCli)
				if err != nil {
					return errors.Wrap(err, "could not remove depot buildx drivers")
				}

				fmt.Println("Successfully uninstalled the Depot Docker CLI plugin")
				return nil
			}

			self, err := os.Executable()
			if err != nil {
				return errors.Wrap(err, "could not find executable")
			}

			if err := installDepotPlugin(dir, self); err != nil {
				return errors.Wrap(err, "could not install depot plugin")
			}

			if err := useDepotBuilderAlias(dir); err != nil {
				return errors.Wrap(err, "could not set depot builder alias")
			}

			err = runConfigureBuildx(dockerCli, project, token)
			if err != nil {
				return errors.Wrap(err, "could not configure buildx")
			}

			fmt.Println("Successfully installed Depot as a Docker CLI plugin")

			return nil
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&uninstall, "uninstall", false, "Remove Docker plugin")
	flags.StringVar(&project, "project", "", "Depot project ID")
	flags.StringVar(&token, "token", "", "Depot API token")

	return cmd
}

func installDepotPlugin(dir, self string) error {
	if err := os.MkdirAll(path.Join(config.Dir(), "cli-plugins"), 0755); err != nil {
		return errors.Wrap(err, "could not create cli-plugins directory")
	}

	symlink := path.Join(config.Dir(), "cli-plugins", "docker-depot")

	err := os.RemoveAll(symlink)
	if err != nil {
		return errors.Wrap(err, "could not remove existing symlink")
	}

	err = os.Symlink(self, symlink)
	if err != nil {
		return errors.Wrap(err, "could not create symlink")
	}

	return nil
}

func useDepotBuilderAlias(dir string) error {
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}

	if cfg.Aliases == nil {
		cfg.Aliases = map[string]string{}
	}
	cfg.Aliases["builder"] = "depot"

	if err := cfg.Save(); err != nil {
		return errors.Wrap(err, "could not write docker config")
	}

	return nil
}

func uninstallDepotPlugin(dir string) error {
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}

	if cfg.Aliases != nil {
		builder, ok := cfg.Aliases["builder"]
		if ok && builder == "depot" {
			delete(cfg.Aliases, "builder")
			if err := cfg.Save(); err != nil {
				return errors.Wrap(err, "could not write docker config")
			}
		}
	}

	buildxPlugin := path.Join(dir, "cli-plugins", "docker-buildx")
	originalBuildxPlugin := path.Join(dir, "cli-plugins", "original-docker-buildx")

	if _, err := os.Stat(originalBuildxPlugin); err == nil {
		err = os.Rename(originalBuildxPlugin, buildxPlugin)
		if err != nil {
			return errors.Wrap(err, "could not replace original docker-buildx plugin")
		}
	}

	depotPlugin := path.Join(dir, "cli-plugins", "docker-depot")

	err = os.RemoveAll(depotPlugin)
	if err != nil {
		return errors.Wrap(err, "could not remove depot plugin")
	}

	return nil
}

func runConfigureBuildx(dockerCli command.Cli, project, token string) error {
	token = helpers.ResolveToken(context.Background(), token)
	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}
	projectName := helpers.ResolveProjectID(project)
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

	return nil
}

type Node struct {
	ProjectID   string
	ContainerID string
}

func ListDepotNodes(ctx context.Context, client dockerclient.APIClient) ([]Node, error) {
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

func StopDepotNodes(ctx context.Context, client dockerclient.APIClient, nodes []Node) error {
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

func RemoveDrivers(ctx context.Context, dockerCli command.Cli) error {
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
	defer release()

	nodeGroups, err := txn.List()
	if err != nil {
		return fmt.Errorf("unable to list node groups: %w", err)
	}

	for _, nodeGroup := range nodeGroups {
		if strings.HasPrefix(nodeGroup.Name, "depot_") {
			err := txn.Remove(nodeGroup.Name)
			if err != nil {
				return fmt.Errorf("unable to remove node group: %w", err)
			}
		}
	}

	return nil
}
