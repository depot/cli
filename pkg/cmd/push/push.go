package push

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	depotapi "github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/ci"
	"github.com/depot/cli/pkg/dockerclient"
	"github.com/depot/cli/pkg/helpers"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	prog "github.com/docker/buildx/util/progress"
	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli/command"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

// NewCmdPush pushes a previously saved build to a registry from the Depot ephemeral registry.
func NewCmdPush() *cobra.Command {
	var (
		token       string
		projectID   string
		buildID     string
		target      string
		progressFmt string
		tags        []string
	)

	cmd := &cobra.Command{
		Use:   "push [flags] [buildID]",
		Short: "Push a project's build from the Depot ephemeral registry to a destination registry",
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
			if progressFmt == prog.PrinterModeAuto && isCI {
				progressFmt = prog.PrinterModePlain
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
				projectID = helpers.ResolveProjectID(projectID)
				projectID, err = selectProjectID(ctx, token, projectID)
				if err != nil {
					return err
				}

				buildID, err = selectBuildID(ctx, token, projectID)
				if err != nil {
					return err
				}
			}

			if len(tags) == 0 {
				return fmt.Errorf("missing tag, please specify a tag with --tag")
			}

			for _, tag := range tags {
				finishPush, err := StartPush(ctx, buildID, tag, token)
				if err != nil {
					return err
				}
				err = Push(ctx, progressFmt, buildID, target, tag, token, dockerCli)
				err = finishPush(err)
				if err != nil {
					return err
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Depot project ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot token")
	cmd.Flags().StringVar(&progressFmt, "progress", "auto", `Set type of progress output ("auto", "plain", "tty", "quiet")`)
	cmd.Flags().StringArrayVarP(&tags, "tag", "t", []string{}, `Name and tag for the pushed image (format: "name:tag")`)
	cmd.Flags().StringVar(&target, "target", "", "bake target")

	return cmd
}

func StartPush(ctx context.Context, buildID, tag, token string) (func(error) error, error) {
	client := depotapi.NewPushClient()
	req := cliv1.StartPushRequest{BuildId: buildID, Tag: tag}
	res, err := client.StartPush(ctx, api.WithAuthentication(connect.NewRequest(&req), token))
	if err != nil {
		return nil, err
	}
	pushID := res.Msg.PushId

	finish := func(err error) error {
		req := cliv1.FinishPushRequest{PushId: pushID, BuildId: buildID}
		if err != nil {
			error := err.Error()
			req.Error = &error
		}
		// Ignore error, we don't want to mask the original error.
		_, _ = client.FinishPush(ctx, api.WithAuthentication(connect.NewRequest(&req), token))
		return err
	}

	return finish, nil
}

func Push(ctx context.Context, progressFmt, buildID, target, tag, token string, dockerCli command.Cli) error {
	reporter, done, err := NewProgress(ctx, progressFmt)
	if err != nil {
		return err
	}
	defer done()

	msg := fmt.Sprintf("[depot] Pushing build %s as %s", buildID, tag)
	if target != "" {
		msg = fmt.Sprintf("[depot] Pushing build %s of target %s as %s", buildID, target, tag)
	}
	logger, finishReporting := reporter.StartLog(msg)

	buildDescriptors, err := GetImageDescriptors(ctx, token, buildID, target, logger)
	if err != nil {
		finishReporting(err)
		return err
	}

	parsedTag, err := ParseTag(tag)
	if err != nil {
		finishReporting(err)
		return err
	}

	fin := logger("Fetching auth token")
	manifest := buildDescriptors.Manifests[0]
	registryToken, err := GetAuthToken(ctx, dockerCli, parsedTag, manifest)
	fin()
	if err != nil {
		finishReporting(err)
		return err
	}

	blobs := append(buildDescriptors.Layers, buildDescriptors.Configs...)
	blobGroup, blobCtx := errgroup.WithContext(ctx)
	for i := range blobs {
		i := i
		blobGroup.Go(func() error {
			blob := blobs[i]
			fin := logger(fmt.Sprintf("Pushing blob %s", blob.Digest.String()))

			blobToPush := &BlobToPush{
				ParsedTag:     parsedTag,
				RegistryToken: registryToken,
				BuildID:       buildID,
				Digest:        blob.Digest,
			}
			err := PushBlob(blobCtx, token, blobToPush)
			fin()
			return err
		})
	}

	err = blobGroup.Wait()
	if err != nil {
		finishReporting(err)
		return err
	}

	// If there are no indices linking together manifests, we assume the manifest should be tagged.
	tagManifest := len(buildDescriptors.Indices) == 0

	for _, manifest := range buildDescriptors.Manifests {
		fin = logger(fmt.Sprintf("Pushing manifest %s", manifest.Digest.String()))

		buf := buildDescriptors.ManifestBytes[manifest.Digest]

		// Tag a manifest with a digest if there are indices.
		tag := parsedTag.Tag
		if !tagManifest {
			tag = manifest.Digest.String()
		}

		err := PushManifest(ctx, registryToken, parsedTag.Refspec, tag, manifest, buf)
		fin()
		if err != nil {
			finishReporting(err)
			return err
		}
	}

	for _, index := range buildDescriptors.Indices {
		fin = logger(fmt.Sprintf("Pushing index %s", index.Digest.String()))

		buf := buildDescriptors.IndexBytes[index.Digest]
		err := PushManifest(ctx, registryToken, parsedTag.Refspec, parsedTag.Tag, index, buf)
		fin()
		if err != nil {
			finishReporting(err)
			return err
		}
	}

	finishReporting(nil)
	return nil
}

func selectProjectID(ctx context.Context, token, projectID string) (string, error) {
	var (
		selectedProject *helpers.SelectedProject
		err             error
	)

	if projectID == "" { // No locally saved depot.json.
		selectedProject, err = helpers.OnboardProject(ctx, token)
		if err != nil {
			return "", err
		}
	} else {
		selectedProject, err = helpers.ProjectExists(ctx, token, projectID)
		if err != nil {
			return "", err
		}
	}
	return selectedProject.ID, nil
}

func selectBuildID(ctx context.Context, token, projectID string) (string, error) {
	client := depotapi.NewBuildClient()

	if !helpers.IsTerminal() {
		depotBuilds, err := helpers.Builds(ctx, token, projectID, client)
		if err != nil {
			return "", err
		}
		_ = depotBuilds.WriteCSV()
		return "", errors.New("build ID must be specified")
	}

	buildID, err := helpers.SelectBuildID(ctx, token, projectID, client)
	if err != nil {
		return "", err
	}

	if buildID == "" {
		return "", errors.New("build ID must be specified")
	}

	return buildID, nil
}
