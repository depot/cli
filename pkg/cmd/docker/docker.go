package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/depot/cli/internal/build"
	"github.com/depot/cli/pkg/buildx/imagetools"
	depotdockerclient "github.com/depot/cli/pkg/dockerclient"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/retry"
	"github.com/docker/buildx/store"
	"github.com/docker/buildx/store/storeutil"
	"github.com/docker/buildx/util/confutil"
	"github.com/docker/buildx/util/dockerutil"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/config"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	goversion "github.com/hashicorp/go-version"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func NewCmdConfigureDocker() *cobra.Command {
	uninstall := false
	var (
		project string
		token   string
	)

	cmd := &cobra.Command{
		Use:   "configure-docker",
		Short: "Configure Docker to use Depot for builds",
		RunE: func(cmd *cobra.Command, args []string) error {
			dockerCli, err := depotdockerclient.NewDockerCLI()
			if err != nil {
				return err
			}

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

			err = runConfigureBuildx(cmd.Context(), dockerCli, project, token)
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
	flags.StringVar(&token, "token", "", "Depot token")

	return cmd
}

func installDepotPlugin(_, self string) error {
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

const depotCLIImageRepo = "public.ecr.aws/depot/cli:"

// driverImageCandidate is one image reference to try for the buildkit driver.
// mutable marks a floating tag (<major>.<minor>, <major>, latest) whose contents
// can change over time; the exact X.Y.Z release tag is immutable.
type driverImageCandidate struct {
	ref     string
	mutable bool
}

// driverImageCandidates returns the driver image references to try, in
// preference order: the exact CLI version, then the floating <major>.<minor>
// and <major> tags.
//
// The exact-version image (public.ecr.aws/depot/cli:<version>) is published by a
// separate, slower pipeline than the one that makes a release resolvable as
// `latest`, so for up to ~an hour after a release the exact tag can 404 while
// the floating tags still point at the previous, driver-compatible patch.
// Falling back to those keeps configure-docker working during that window.
// See DEP-5314.
//
// Floating fallbacks are only added for a clean release version (X.Y.Z, no
// prerelease). For anything else (dev, prerelease, `latest`) the single exact
// candidate is itself treated as mutable.
func driverImageCandidates(cliVersion string) []driverImageCandidate {
	v, err := goversion.NewVersion(cliVersion)
	cleanRelease := err == nil && v.Prerelease() == ""

	exact := depotCLIImageRepo + cliVersion
	candidates := []driverImageCandidate{{ref: exact, mutable: !cleanRelease}}
	if !cleanRelease {
		return candidates
	}

	seg := v.Segments()
	if mm := fmt.Sprintf("%s%d.%d", depotCLIImageRepo, seg[0], seg[1]); mm != exact {
		candidates = append(candidates, driverImageCandidate{ref: mm, mutable: true})
	}
	if mj := fmt.Sprintf("%s%d", depotCLIImageRepo, seg[0]); mj != exact {
		candidates = append(candidates, driverImageCandidate{ref: mj, mutable: true})
	}
	return candidates
}

// resolveDriverImageForBuild resolves the driver image for the current CLI
// build. It maps the dev version to `latest`, picks the best available candidate
// (falling back to floating tags during the post-release publish window), warns
// on fallback, and returns an immutable reference safe to persist and to reuse
// by string comparison. Shared by configure-docker and UpdateDrivers so both
// make the same decision.
func resolveDriverImageForBuild(ctx context.Context, dockerCli command.Cli) (string, error) {
	version := build.Version
	if version == "0.0.0-dev" {
		version = "latest"
	}

	candidates := driverImageCandidates(version)
	ref, fellBack, err := resolveDriverImage(ctx, dockerCli, candidates)
	if err != nil {
		return "", err
	}
	if fellBack {
		fmt.Fprintf(os.Stderr, "depot: driver image %s unavailable, falling back to %s\n", candidates[0].ref, ref)
	}
	return ref, nil
}

// resolveDriverImage returns an immutable reference for the first candidate that
// can be pulled, retrying each a few times to ride out transient registry
// errors before falling through to the next. A mutable tag is pinned to the
// digest just pulled so a later move of the tag can't leave the persisted node
// config or a reused driver container pointing at different contents. fellBack
// reports whether a candidate other than the exact tag was used.
func resolveDriverImage(ctx context.Context, dockerCli command.Cli, candidates []driverImageCandidate) (ref string, fellBack bool, err error) {
	chosen, err := selectDriverImage(candidates, func(c driverImageCandidate) error {
		// A mutable tag's local copy may be stale, so force a registry pull to
		// fetch the current image before we pin it.
		return retry.Retry(func() error {
			return downloadImage(ctx, dockerCli, c.ref, c.mutable)
		}, 3)
	})
	if err != nil {
		return "", false, err
	}

	fellBack = chosen.ref != candidates[0].ref
	if !chosen.mutable {
		return chosen.ref, fellBack, nil
	}
	if pinned, perr := pinnedImageRef(ctx, dockerCli, chosen.ref); perr == nil {
		return pinned, fellBack, nil
	}
	// If the digest can't be read, the tag is still usable; accept the small
	// staleness risk rather than failing the resolve.
	return chosen.ref, fellBack, nil
}

// selectDriverImage returns the first candidate for which pull succeeds, trying
// them in order, or the last error if every candidate fails.
func selectDriverImage(candidates []driverImageCandidate, pull func(driverImageCandidate) error) (driverImageCandidate, error) {
	var err error
	for _, c := range candidates {
		if err = pull(c); err == nil {
			return c, nil
		}
	}
	return driverImageCandidate{}, err
}

// pinnedImageRef returns imageName rewritten to its immutable repo@sha256 digest
// form, using the digest of the locally present image.
func pinnedImageRef(ctx context.Context, dockerCli command.Cli, imageName string) (string, error) {
	inspect, _, err := dockerCli.Client().ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		return "", err
	}

	// Split off the tag to get the bare repo (a registry port colon, if any,
	// sorts before the last path separator, so only the tag colon matches here).
	repo := imageName
	if i := strings.LastIndex(imageName, ":"); i > strings.LastIndex(imageName, "/") {
		repo = imageName[:i]
	}
	for _, rd := range inspect.RepoDigests {
		if strings.HasPrefix(rd, repo+"@") {
			return rd, nil
		}
	}
	return "", fmt.Errorf("no repo digest found for %s", imageName)
}

// hasDepotDriverNode reports whether any node group already uses a depot driver
// image, i.e. whether there is anything for UpdateDrivers to update.
func hasDepotDriverNode(nodeGroups []*store.NodeGroup) bool {
	for _, nodeGroup := range nodeGroups {
		for _, node := range nodeGroup.Nodes {
			image := node.DriverOpts["image"]
			if strings.HasPrefix(image, "ghcr.io/depot/cli") || strings.HasPrefix(image, "public.ecr.aws/depot/cli") {
				return true
			}
		}
	}
	return false
}

func runConfigureBuildx(ctx context.Context, dockerCli command.Cli, project, token string) error {
	var err error
	token, err = helpers.ResolveProjectAuth(ctx, token)
	if err != nil {
		return err
	}

	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}
	projectName := helpers.ResolveProjectID(project)
	if projectName == "" {
		return errors.Errorf("unknown project ID (run `depot init` or use --project or $DEPOT_PROJECT_ID)")
	}

	// Resolve the driver image before opening the store transaction, so the
	// registry pulls (and release-window fallback retries) don't run while the
	// cross-process store lock is held (DEP-5314).
	image, err := resolveDriverImageForBuild(ctx, dockerCli)
	if err != nil {
		return fmt.Errorf("unable to create driver container: %w", err)
	}

	configStore, err := store.New(confutil.ConfigDir(dockerCli))
	if err != nil {
		return fmt.Errorf("unable to create docker configuration store: %w", err)
	}
	txn, release, err := configStore.Txn()
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
					{
						Architecture: "arm",
						OS:           "linux",
						Variant:      "v7",
					},
					{
						Architecture: "arm",
						OS:           "linux",
						Variant:      "v8",
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

	// DEPOT: we override the buildx Txn.Save() as its atomic write file
	// can leave temporary files within the instance directory thus causing
	// buildx to fail.
	if err := DepotSaveNodes(confutil.ConfigDir(dockerCli), ng); err != nil {
		return fmt.Errorf("unable to save node group: %w", err)
	}

	global := false
	dflt := false
	if err := txn.SetCurrent(endpoint, nodeName, global, dflt); err != nil {
		return fmt.Errorf("unable to use node group: %w", err)
	}

	for _, arch := range []string{"amd64", "arm64"} {
		// Occasionally Docker fails to pull the driver containers on the first try. We retry up to 3 times.
		err = retry.Retry(func() error {
			return Bootstrap(ctx, dockerCli, image, projectName, token, arch)
		}, 3)
		if err != nil {
			return fmt.Errorf("unable create driver container: %w", err)
		}
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
	containers, err := client.ContainerList(ctx, dockertypes.ContainerListOptions{
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
		err := client.ContainerRemove(ctx, node.ContainerID, dockertypes.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		if err != nil {
			return err
		}
	}

	return nil
}

func UpdateDrivers(ctx context.Context, dockerCli command.Cli) error {
	// Check whether there is anything to update before doing any registry work.
	// This runs in a background goroutine on every build, so a machine with no
	// depot driver nodes must not pull the driver image. Read the store, then
	// release the lock before the (network) resolve below so it isn't held
	// during pulls (DEP-5314).
	txn, release, err := storeutil.GetStore(dockerCli)
	if err != nil {
		return fmt.Errorf("unable to get docker store: %w", err)
	}
	nodeGroups, err := txn.List()
	if err != nil {
		release()
		return fmt.Errorf("unable to list node groups: %w", err)
	}
	hasDepot := hasDepotDriverNode(nodeGroups)
	release()
	if !hasDepot {
		return nil
	}

	// Resolve the driver image before tearing down the existing drivers, so a
	// failed resolve leaves the working drivers in place rather than deleting
	// them (DEP-5314).
	resolvedImage, err := resolveDriverImageForBuild(ctx, dockerCli)
	if err != nil {
		return fmt.Errorf("unable to resolve driver image: %w", err)
	}

	nodes, err := ListDepotNodes(ctx, dockerCli.Client())
	if err != nil {
		return err
	}
	if err := StopDepotNodes(ctx, dockerCli.Client(), nodes); err != nil {
		return err
	}

	txn, release, err = storeutil.GetStore(dockerCli)
	if err != nil {
		return fmt.Errorf("unable to get docker store: %w", err)
	}
	defer release()

	nodeGroups, err = txn.List()
	if err != nil {
		return fmt.Errorf("unable to list node groups: %w", err)
	}

	for _, nodeGroup := range nodeGroups {
		var save bool
		for i, node := range nodeGroup.Nodes {
			image := node.DriverOpts["image"]
			if strings.HasPrefix(image, "ghcr.io/depot/cli") || strings.HasPrefix(image, "public.ecr.aws/depot/cli") {
				nodeGroup.Nodes[i].DriverOpts["image"] = resolvedImage
				save = true

				projectName := node.DriverOpts["env.DEPOT_PROJECT_ID"]
				token := node.DriverOpts["env.DEPOT_TOKEN"]
				platform := node.DriverOpts["env.DEPOT_PLATFORM"]
				_ = Bootstrap(ctx, dockerCli, resolvedImage, projectName, token, platform)
			}

		}

		if save {
			if err := txn.Save(nodeGroup); err != nil {
				return fmt.Errorf("unable to save node group: %w", err)
			}
		}
	}

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

// Bootstrap is similar to the buildx bootstrap.  It is used to create (but not start) the container.
// We did this because docker compose and buildx have race conditions that try to start the container
// more than one time: https://github.com/docker/buildx/pull/2000
func Bootstrap(ctx context.Context, dockerCli command.Cli, imageName, projectName, token, platform string) error {
	err := DownloadImage(ctx, dockerCli, imageName)
	if err != nil {
		return fmt.Errorf("unable to download image: %w", err)
	}

	return CreateContainer(ctx, dockerCli, projectName, platform, imageName, token)
}

func DownloadImage(ctx context.Context, dockerCli command.Cli, imageName string) error {
	return downloadImage(ctx, dockerCli, imageName, false)
}

// downloadImage pulls imageName. When forcePull is false, an image already
// present locally is accepted without contacting the registry. When true, the
// registry is always consulted, so a mutable/floating tag (e.g. <major>.<minor>)
// is refreshed to its current digest instead of trusting a possibly stale local
// copy.
func downloadImage(ctx context.Context, dockerCli command.Cli, imageName string, forcePull bool) error {
	client := dockerCli.Client()

	if !forcePull {
		images, err := client.ImageList(ctx, dockertypes.ImageListOptions{
			Filters: filters.NewArgs(filters.Arg("reference", imageName)),
		})
		if err == nil && len(images) > 0 {
			return nil
		}
	}

	ra, err := imagetools.RegistryAuthForRef(imageName, dockerCli.ConfigFile())
	if err != nil {
		return err
	}

	rc, err := client.ImageCreate(ctx, imageName, dockertypes.ImageCreateOptions{
		RegistryAuth: ra,
	})
	if err != nil {
		return fmt.Errorf("unable to download image: %w", err)
	}

	_, err = io.Copy(io.Discard, rc)
	return err
}

func CreateContainer(ctx context.Context, dockerCli command.Cli, projectName string, platform string, imageName string, token string) error {
	client := dockerCli.Client()
	name := "buildx_buildkit_depot_" + projectName + "_" + platform

	driverContainer, err := client.ContainerInspect(ctx, name)
	if err == nil {
		if driverContainer.Config.Image == imageName {
			return nil
		}

		err := client.ContainerRemove(ctx, driverContainer.ID, dockertypes.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		if err != nil {
			return fmt.Errorf("unable to remove container: %w", err)
		}

		_, _ = client.ImageRemove(ctx, driverContainer.Config.Image, dockertypes.ImageRemoveOptions{})
	}

	cfg := &container.Config{
		Image: imageName,
		Env: []string{
			"DEPOT_PROJECT_ID=" + projectName,
			"DEPOT_TOKEN=" + token,
			"DEPOT_PLATFORM=" + platform,
		},
		Cmd: []string{"buildkitd"},
	}

	useInit := true
	hc := &container.HostConfig{
		Privileged: true,
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeVolume,
				Source: "buildx_buildkit_depot_" + projectName + "_" + platform + "_state",
				Target: confutil.DefaultBuildKitStateDir,
			},
		},
		Init: &useInit,
	}

	if info, err := client.Info(ctx); err == nil {
		if info.CgroupDriver == "cgroupfs" {

			hc.CgroupParent = "/docker/buildx"
		}

		secOpts, err := dockertypes.DecodeSecurityOptions(info.SecurityOptions)
		if err != nil {
			return err
		}
		for _, f := range secOpts {
			if f.Name == "userns" {
				hc.UsernsMode = "host"
				break
			}
		}

	}

	_, err = client.ContainerCreate(ctx, cfg, hc, &network.NetworkingConfig{}, nil, name)
	if err != nil {
		return fmt.Errorf("unable to create container: %w", err)
	}

	return nil
}

// DEPOT: we override the buildx Txn.Save() as its atomic write file
// can leave temporary files within the instance directory thus causing
// buildx to fail.
func DepotSaveNodes(configDir string, ng *store.NodeGroup) (err error) {
	name, err := store.ValidateName(ng.Name)
	if err != nil {
		return err
	}

	octets, err := json.Marshal(ng)
	if err != nil {
		return err
	}

	instancesDir := filepath.Join(configDir, "instances")
	fileName := filepath.Join(instancesDir, name)

	// DEPOT: this is the key change for saving the nodes.
	// Previously, it would save in the instances directory, but
	// those files would then be read by the Txn.List()/Txn.NodeGroupByName()
	// methods and thus would fail.
	//
	// Instead, we save the file to the configDir and then rename it.
	// CreateTemp creates a file with 0600 perms.
	f, err := os.CreateTemp(configDir, ".tmp-"+filepath.Base(fileName))
	if err != nil {
		return err
	}

	// Require that the file be removed on error.
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()

	n, err := f.Write(octets)
	if err != nil {
		return err
	}

	if n != len(octets) {
		err = io.ErrShortWrite
		return err
	}

	err = f.Sync()
	if err != nil {
		return err
	}

	err = f.Close()
	if err != nil {
		return err
	}

	return os.Rename(f.Name(), fileName)
}
