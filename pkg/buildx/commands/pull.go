package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/buildx/build"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/moby/buildkit/client"
)

// Options to download from the Depot hosted registry and tag the image with the user provide tag.
type PullOptions struct {
	UserTags []string // Tags the user wishes the image to have.
	Quiet    bool     // No logs plz
}

// WithDepotImagePull updates buildOpts to push to the depot user's personal registry.
// allowing us to pull layers in parallel from the depot registry.
func WithDepotImagePull(buildOpts map[string]build.Options, depotOpts DepotOptions, progressMode string) (map[string]build.Options, map[string]PullOptions) {
	// For backwards compatibility if the API does not support the depot registry,
	// we use the previous buildx behavior of pulling the image via the output docker.
	// NOTE: this means that a single tar will be sent from buildkit to the client and
	// imported into the docker daemon.  This is quite slow.
	if !depotOpts.useLocalRegistry {
		for key, buildOpt := range buildOpts {
			if len(buildOpt.Exports) != 0 {
				continue // assume that exports already has a docker export.
			}
			buildOpt.Exports = []client.ExportEntry{
				{
					Type:  "docker",
					Attrs: map[string]string{},
				},
			}
			buildOpts[key] = buildOpt
		}
		return buildOpts, map[string]PullOptions{}
	}

	toPull := make(map[string]PullOptions)
	for target, buildOpt := range buildOpts {
		// Gather all tags the user specifies for this image.
		userTags := buildOpt.Tags

		var shouldPull bool
		// As of today (2023-03-15), buildx only supports one export.
		for _, export := range buildOpt.Exports {
			// Only pull if the user asked for an image export.
			if export.Type == "image" {
				shouldPull = true
				if name, ok := export.Attrs["name"]; ok {
					// "name" is a comma separated list of tags to apply to the image.
					userTags = append(userTags, strings.Split(name, ",")...)
				}
			}
		}

		// If the user did not specify an image export, we add one.
		// This happens when the user specifies `--load` rather than an `--output`
		if len(buildOpt.Exports) == 0 {
			shouldPull = true
			buildOpt.Exports = []client.ExportEntry{{Type: "image"}}
		}

		buildOpts[target] = buildOpt

		if shouldPull {
			// When we pull we need at least one user tag as no tags means that
			// it would otherwise get removed.
			if len(userTags) == 0 {
				defaultImageName := fmt.Sprintf("depot-project-%s:build-%s", depotOpts.project, depotOpts.buildID)
				if target != defaultTargetName {
					defaultImageName = fmt.Sprintf("%s-%s", defaultImageName, target)
				}

				userTags = append(userTags, defaultImageName)
			}

			pullOpt := PullOptions{
				UserTags: userTags,
				Quiet:    progressMode == progress.PrinterModeQuiet,
			}
			toPull[target] = pullOpt
		}
	}

	// Add oci-mediatypes for any image build regardless of whether we are pulling.
	// This gives us more flexibility for future options like estargz.
	for target, buildOpt := range buildOpts {
		for i, export := range buildOpt.Exports {
			if export.Type == "image" {
				if export.Attrs == nil {
					export.Attrs = map[string]string{}
				}

				export.Attrs["oci-mediatypes"] = "true"
			}
			buildOpt.Exports[i] = export
		}
		buildOpts[target] = buildOpt
	}

	return buildOpts, toPull
}

func PullImages(ctx context.Context, dockerapi docker.APIClient, imageName string, opts PullOptions, w progress.Writer) error {
	tags := strings.Join(opts.UserTags, ",")
	return progress.Wrap(fmt.Sprintf("pulling %s", tags), w.Write, func(logger progress.SubLogger) error {
		return ImagePullPrivileged(ctx, dockerapi, imageName, opts, logger)
	})
}

func ImagePullPrivileged(ctx context.Context, dockerapi docker.APIClient, imageName string, opts PullOptions, logger progress.SubLogger) error {
	responseBody, err := dockerapi.ImagePull(ctx, imageName, types.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer responseBody.Close()

	if opts.Quiet {
		_, err := io.Copy(io.Discard, responseBody)
		return err
	} else {
		if err := printPull(ctx, responseBody, logger); err != nil {
			return err
		}
	}

	// Swap the depot tag with the user-specified tags by adding the user tag
	// and removing the depot one.
	for _, userTag := range opts.UserTags {
		if err := dockerapi.ImageTag(ctx, imageName, userTag); err != nil {
			return err
		}
	}

	// PruneChildren is false to preserve the image if no tag was specified.
	rmOpts := types.ImageRemoveOptions{PruneChildren: false}
	_, err = dockerapi.ImageRemove(ctx, imageName, rmOpts)
	if err != nil {
		return err
	}

	return nil
}

func printPull(ctx context.Context, rc io.ReadCloser, l progress.SubLogger) error {
	started := map[string]*client.VertexStatus{}

	defer func() {
		for _, st := range started {
			if st.Completed == nil {
				now := time.Now()
				st.Completed = &now
				l.SetStatus(st)
			}
		}
	}()

	dec := json.NewDecoder(rc)

	var (
		parsedError error
		jm          jsonmessage.JSONMessage
	)

	for {
		if err := dec.Decode(&jm); err != nil {
			if parsedError != nil {
				return parsedError
			}
			if err == io.EOF {
				break
			}
			return err
		}

		if jm.Error != nil {
			parsedError = jm.Error
		}

		if jm.ID == "" {
			continue
		}

		id := "pulling layer " + jm.ID
		st, ok := started[id]
		if !ok {
			if jm.Progress != nil || strings.HasPrefix(jm.Status, "Pulling") {
				now := time.Now()
				st = &client.VertexStatus{
					ID:      id,
					Started: &now,
				}
				started[id] = st
			} else {
				continue
			}
		}
		st.Timestamp = time.Now()
		if jm.Progress != nil {
			st.Current = jm.Progress.Current
			st.Total = jm.Progress.Total
		}
		if jm.Error != nil {
			now := time.Now()
			st.Completed = &now
		}

		if jm.Status == "Pull complete" {
			now := time.Now()
			st.Completed = &now
			st.Current = st.Total
		}
		l.SetStatus(st)
	}
	return nil
}
