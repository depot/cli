package build

import (
	"fmt"
	"os"

	"github.com/depot/cli/pkg/config"
	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli/command"
	cliflags "github.com/docker/cli/cli/flags"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	_ "github.com/depot/cli/pkg/buildxdriver"
	_ "github.com/moby/buildkit/util/tracing/detect/delegated"
	_ "github.com/moby/buildkit/util/tracing/env"
)

func NewCmdBuild() *cobra.Command {
	options := newBuildOptions()

	cmd := &cobra.Command{
		Use:   "build [OPTIONS] PATH | URL | -",
		Short: "run a Docker build on Depot",
		Args:  cli.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if options.project == "" {
				options.project = os.Getenv("DEPOT_PROJECT_ID")
			}
			if options.project == "" {
				return errors.Errorf("unknown project ID (use --project or $DEPOT_PROJECT_ID)")
			}

			if options.token == "" {
				options.token = config.GetApiToken()
			}
			if options.token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			dockerCli, err := command.NewDockerCli()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			opts := cliflags.NewClientOptions()
			err = dockerCli.Initialize(opts)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}

			options.contextPath = args[0]
			return runBuild(dockerCli, options)
		},
	}

	var platformsDefault []string
	if v := os.Getenv("DOCKER_DEFAULT_PLATFORM"); v != "" {
		platformsDefault = []string{v}
	}

	flags := cmd.Flags()

	// Depot options
	flags.StringVar(&options.project, "project", "", "Depot project ID")
	flags.StringVar(&options.token, "token", "", "Depot API token")

	// `docker buildx build` options
	flags.StringSliceVar(&options.extraHosts, "add-host", []string{}, "Add a custom host-to-IP mapping (format: `host:ip`)")
	flags.StringSliceVar(&options.allow, "allow", []string{}, "Allow extra privileged entitlement (e.g., `network.host`, `security.insecure`)")
	flags.StringArrayVar(&options.buildArgs, "build-arg", []string{}, "Set build-time variables")
	flags.StringArrayVar(&options.cacheFrom, "cache-from", []string{}, "External cache sources (e.g., `user/app:cache`, `type=local,src=path/to/dir`)")
	flags.StringArrayVar(&options.cacheTo, "cache-to", []string{}, "Cache export destinations (e.g., `user/app:cache`, `type=local,dest=path/to/dir`)")
	flags.StringVar(&options.cgroupParent, "cgroup-parent", "", "Optional parent cgroup for the container")
	flags.StringVarP(&options.dockerfileName, "file", "f", "", "Name of the Dockerfile (default: `PATH/Dockerfile`)")
	flags.StringVar(&options.imageIDFile, "iidfile", "", "Write the image ID to the file")
	flags.StringArrayVar(&options.labels, "label", []string{}, "Set metadata for an image")
	flags.BoolVar(&options.exportLoad, "load", false, "Shorthand for `--output=type=docker`")
	flags.StringVar(&options.networkMode, "network", "default", "Set the networking mode for the RUN instructions during build")
	flags.StringArrayVarP(&options.outputs, "output", "o", []string{}, "Output destination (format: `type=local,dest=path`)")
	flags.StringArrayVar(&options.platforms, "platform", platformsDefault, "Set target platform for build")
	flags.BoolVar(&options.exportPush, "push", false, "Shorthand for `--output=type=registry`")
	flags.BoolVarP(&options.quiet, "quiet", "q", false, "Suppress the build output and print image ID on success")
	flags.StringArrayVar(&options.secrets, "secret", []string{}, "Secret file to expose to the build (format: `id=mysecret,src=/local/secret`)")
	flags.Var(&options.shmSize, "shm-size", "Size of `/dev/shm`")
	flags.StringArrayVar(&options.ssh, "ssh", []string{}, "SSH agent socket or keys to expose to the build (format: `default|<id>[=<socket>|<key>[,<key>]]`)")
	flags.StringArrayVarP(&options.tags, "tag", "t", []string{}, "Name and optionally a tag (format: `name:tag`)")
	flags.StringVar(&options.target, "target", "", "Set the target build stage to build.")
	flags.Var(options.ulimits, "ulimit", "Ulimit options")

	options.commonOptions.noCache = flags.Bool("no-cache", false, "Do not use cache when building the image")
	flags.StringVar(&options.commonOptions.progress, "progress", "auto", "Set type of progress output (`auto`, `plain`, `tty`). Use plain to show container output")
	options.commonOptions.pull = flags.Bool("pull", false, "Always attempt to pull a newer version of the image")
	flags.StringVar(&options.commonOptions.metadataFile, "metadata-file", "", "Write build result metadata to the file")

	return cmd
}