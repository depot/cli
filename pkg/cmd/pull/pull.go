package pull

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	depotapi "github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/ci"
	"github.com/depot/cli/pkg/dockerclient"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/load"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	prog "github.com/docker/buildx/util/progress"
	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli/command"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

func NewCmdPull() *cobra.Command {
	var (
		token     string
		projectID string
		platform  string
		buildID   string
		progress  string
		userTags  []string
		targets   []string
	)

	cmd := &cobra.Command{
		Use:   "pull [flags] [buildID]",
		Short: "Pull a project's build from the Depot registry",
		Args:  cli.RequiresMaxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dockerCli, err := dockerclient.NewDockerCLI()
			if err != nil {
				return err
			}

			if len(args) > 0 {
				buildID = args[0]
			}
			_, isCI := ci.Provider()
			if progress == prog.PrinterModeAuto && isCI {
				progress = prog.PrinterModePlain
			}

			ctx := cmd.Context()

			token, err := helpers.ResolveToken(ctx, token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			if buildID == "" {
				var selectedProject *helpers.SelectedProject
				projectID = helpers.ResolveProjectID(projectID)
				if projectID == "" { // No locally saved depot.json.
					selectedProject, err = helpers.OnboardProject(ctx, token)
					if err != nil {
						return err
					}
				} else {
					selectedProject, err = helpers.ProjectExists(ctx, token, projectID)
					if err != nil {
						return err
					}
				}
				projectID = selectedProject.ID

				client := depotapi.NewBuildClient()

				if !helpers.IsTerminal() {
					depotBuilds, err := helpers.Builds(ctx, token, projectID, client)
					if err != nil {
						return err
					}
					_ = depotBuilds.WriteCSV()
					return fmt.Errorf("build ID must be specified")
				}

				buildID, err = helpers.SelectBuildID(ctx, token, projectID, client)
				if err != nil {
					return err
				}

				if buildID == "" {
					return fmt.Errorf("build ID must be specified")
				}
			}

			client := depotapi.NewBuildClient()
			req := &cliv1.GetPullInfoRequest{BuildId: buildID}
			res, err := client.GetPullInfo(ctx, depotapi.WithAuthentication(connect.NewRequest(req), token))
			if err != nil {
				return err
			}

			buildOptions := res.Msg.Options
			savedForLoad := res.Msg.SaveForLoad
			if len(buildOptions) > 0 && !isSavedBuild(buildOptions, savedForLoad) {
				return fmt.Errorf("build %s is not a saved build. To use the registry use --save when building", buildID)
			}

			if isBake(buildOptions) {
				return pullBake(ctx, dockerCli, res.Msg, targets, userTags, platform, progress)
			} else {
				return pullBuild(ctx, dockerCli, res.Msg, userTags, platform, progress)
			}
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Depot project ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot token")
	cmd.Flags().StringVar(&platform, "platform", "", `Pulls image for specific platform ("linux/amd64", "linux/arm64")`)
	cmd.Flags().StringSliceVarP(&userTags, "tag", "t", nil, "Optional tags to apply to the image")
	cmd.Flags().StringVar(&progress, "progress", "auto", `Set type of progress output ("auto", "plain", "tty", "quiet")`)
	cmd.Flags().StringSliceVar(&targets, "target", nil, "Pulls image for specific bake targets")

	return cmd
}

func pullBuild(ctx context.Context, dockerCli command.Cli, msg *cliv1.GetPullInfoResponse, userTags []string, platform string, progress string) error {
	pull := buildPullOpt(msg, userTags, platform, progress)
	printer, cancel, err := buildPrinter(ctx, pull, progress)
	if err != nil {
		return err
	}
	defer func() {
		cancel()
		_ = printer.Wait()
	}()

	return load.PullImages(ctx, dockerCli.Client(), pull.imageName, pull.pullOptions, printer)
}

func pullBake(ctx context.Context, dockerCli command.Cli, msg *cliv1.GetPullInfoResponse, targets, userTags []string, platform string, progress string) error {
	err := validateTargets(targets, msg)
	if err != nil {
		return err
	}
	pullOpts := bakePullOpts(msg, targets, userTags, platform, progress)
	printer, cancel, err := bakePrinter(ctx, pullOpts, progress)
	if err != nil {
		return err
	}
	defer func() {
		cancel()
		_ = printer.Wait()
	}()

	eg, ctx2 := errgroup.WithContext(ctx)
	// Three concurrent pulls at a time to avoid overwhelming the registry.
	eg.SetLimit(3)
	for _, p := range pullOpts {
		func(imageName string, pullOptions load.PullOptions) {
			eg.Go(func() error {
				return load.PullImages(ctx2, dockerCli.Client(), imageName, pullOptions, printer)
			})
		}(p.imageName, p.pullOptions)
	}
	return eg.Wait()
}
