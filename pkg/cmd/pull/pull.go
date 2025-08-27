package pull

import (
	"context"
	"fmt"
	"strings"

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

const (
	depotRegistry = "registry.depot.dev"
)

func NewCmdPull() *cobra.Command {
	var (
		token     string
		projectID string
		platform  string
		param     string
		progress  string
		userTags  []string
		targets   []string
	)

	cmd := &cobra.Command{
		Use:   "pull [flags] [buildID|tag]",
		Short: "Pull a project's build from the Depot registry",
		Args:  cli.RequiresMaxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dockerCli, err := dockerclient.NewDockerCLI()
			if err != nil {
				return err
			}

			if len(args) > 0 {
				param = args[0]
			}
			_, isCI := ci.Provider()
			if progress == prog.PrinterModeAuto && isCI {
				progress = prog.PrinterModePlain
			}

			ctx := cmd.Context()

			token, err = helpers.ResolveProjectAuth(ctx, token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			if param == "" {
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

				param, err = helpers.SelectBuildID(ctx, token, projectID, client)
				if err != nil {
					return err
				}

				if param == "" {
					return fmt.Errorf("build ID or tag must be specified")
				}
			}

			// Check if the buildID is actually a registry reference or tag
			if strings.HasPrefix(param, depotRegistry+"/") {
				// Extract project ID and tag from the reference
				projectID, tag := extractProjectIDAndTag(param)
				return pullByTag(ctx, dockerCli, token, projectID, tag, userTags, platform, progress)
			}

			// Check if the param is in the format "projectID:tag"
			if strings.Contains(param, ":") && !strings.HasPrefix(param, depotRegistry+"/") {
				parts := strings.SplitN(param, ":", 2)
				if len(parts) == 2 {
					projectID = parts[0]
					tag := parts[1]
					return pullByTag(ctx, dockerCli, token, projectID, tag, userTags, platform, progress)
				}
			}

			// Try to get build info first (build ID approach)
			client := depotapi.NewBuildClient()
			req := &cliv1.GetPullInfoRequest{BuildId: param}
			res, err := client.GetPullInfo(ctx, depotapi.WithAuthentication(connect.NewRequest(req), token))
			if err != nil {
				// If GetPullInfo fails, try the direct tag approach
				return pullByTag(ctx, dockerCli, token, projectID, param, userTags, platform, progress)
			}

			buildOptions := res.Msg.Options
			savedForLoad := res.Msg.SaveForLoad
			if len(buildOptions) > 0 && !isSavedBuild(buildOptions, savedForLoad) {
				return fmt.Errorf("build %s is not a saved build. To use the registry use --save when building", param)
			}

			if isBake(buildOptions) {
				return pullBake(ctx, dockerCli, res.Msg, targets, userTags, platform, progress)
			}

			return pullBuild(ctx, dockerCli, res.Msg, userTags, platform, progress)
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

// extractProjectIDAndTag extracts project ID and tag from a registry reference
func extractProjectIDAndTag(reference string) (projectID string, tag string) {
	// Remove the registry prefix
	projectAndTag := strings.TrimPrefix(reference, depotRegistry+"/")

	// Split on colon to separate project ID and tag
	parts := strings.SplitN(projectAndTag, ":", 2)
	if len(parts) != 2 {
		return "", reference
	}

	return parts[0], parts[1]
}

// pullByTag handles pulling an image directly by tag when build ID approach fails
func pullByTag(ctx context.Context, dockerCli command.Cli, token, projectID, tag string, userTags []string, platform, progress string) error {
	client := depotapi.NewBuildClient()

	// Get pull token for the project
	req := &cliv1.GetPullTokenRequest{ProjectId: &projectID}
	res, err := client.GetPullToken(ctx, depotapi.WithAuthentication(connect.NewRequest(req), token))
	if err != nil {
		return fmt.Errorf("failed to get pull token for project %s: %w", projectID, err)
	}

	// Construct the full image reference
	imageName := fmt.Sprintf("%s/%s:%s", depotRegistry, projectID, tag)

	// Set up pull options
	serverAddress := depotRegistry
	username := "x-token"
	opts := load.PullOptions{
		UserTags:      userTags,
		Quiet:         progress == prog.PrinterModeQuiet,
		KeepImage:     true,
		Username:      &username,
		Password:      &res.Msg.Token,
		ServerAddress: &serverAddress,
	}
	if platform != "" {
		opts.Platform = &platform
	}

	// Create a simple pull struct
	pull := &pull{
		imageName:   imageName,
		pullOptions: opts,
	}

	// Set up printer
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
