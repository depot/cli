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
	"github.com/docker/cli/cli/command"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/moby/buildkit/client"
)

// Options to download from the Depot hosted registry and tag the image with the user provide tag.
type PullOptions struct {
	UserTags           []string // Tags the user wishes the image to have.
	DepotTag           string   // Tag used in depot hosted registry
	DepotRegistryURL   string   // URL of depot hosted registry
	DepotRegistryToken string   // Token used to authenticate with depot hosted registry
	Quiet              bool     // No logs plz
}

// DepotLocalImagePull configures image exports to push to the depot user's personal registry.
// allowing us to pull layers in parallel from the depot registry.
func DepotLocalImagePull(buildOpts map[string]build.Options, buildID, token string, progressMode string) []PullOptions {
	toPull := []PullOptions{}
	for _, buildOpt := range buildOpts {
		// TODO: figureout the best depotImageName.  Something from the builtOpt?
		depotImageName := fmt.Sprintf("ecr.io/your-registry/your-image:%s", buildID)

		userTags := buildOpt.Tags

		var shouldPull bool
		if len(buildOpt.Exports) == 0 {
			shouldPull = true
			buildOpt.Exports = []client.ExportEntry{
				{Type: "image", Attrs: map[string]string{"name": depotImageName, "push": "true"}}}
		} else {
			// As of today (2023-03-15), buildx only supports one export.
			for _, export := range buildOpt.Exports {
				// Only pull if the user asked for an import export.
				if export.Type == "image" {
					shouldPull = true
					if name, ok := export.Attrs["name"]; ok {
						// "name" is a comma separated list of tags to apply to the image.
						userTags = append(userTags, strings.Split(name, ",")...)

						// Also, push to user's private depot registry as well as the original registry.
						export.Attrs["name"] = fmt.Sprintf("%s,%s", name, depotImageName)
						export.Attrs["push"] = "true"
					} else {
						export.Attrs["name"] = depotImageName
						export.Attrs["push"] = "true"
					}
				}
			}
		}

		if shouldPull {
			pullOpt := PullOptions{
				UserTags:           userTags,
				DepotTag:           depotImageName,
				DepotRegistryURL:   "https://ecr.io", // TODO:
				DepotRegistryToken: token,
				Quiet:              progressMode == progress.PrinterModeQuiet,
			}
			toPull = append(toPull, pullOpt)
		}
	}

	return toPull
}

func PullImages(ctx context.Context, dockerapi docker.APIClient, opts PullOptions, w progress.Writer) error {
	pw := progress.WithPrefix(w, "default", false)

	tags := strings.Join(opts.UserTags, ",")
	err := progress.Wrap(fmt.Sprintf("pulling %s", tags), pw.Write, func(l progress.SubLogger) error {
		return ImagePullPrivileged(ctx, dockerapi, opts, l)
	})

	if err != nil {
		return err
	}

	progress.Write(pw, fmt.Sprintf("pulled %s", tags), func() error { return nil })

	return nil
}

func ImagePullPrivileged(ctx context.Context, dockerapi docker.APIClient, opts PullOptions, l progress.SubLogger) error {
	authConfig := types.AuthConfig{
		ServerAddress: opts.DepotRegistryURL,
		RegistryToken: opts.DepotRegistryToken,
	}

	encodedAuth, err := command.EncodeAuthToBase64(authConfig)
	if err != nil {
		return err
	}

	responseBody, err := dockerapi.ImagePull(ctx, opts.DepotTag, types.ImagePullOptions{
		RegistryAuth: encodedAuth,
	})
	if err != nil {
		return err
	}
	defer responseBody.Close()

	if opts.Quiet {
		_, err := io.Copy(io.Discard, responseBody)
		return err
	} else {
		if err := printPull(ctx, responseBody, l); err != nil {
			return err
		}
	}

	// Swap the depot tag with the user-specified tags by adding the user tag
	// and removing the depot one.
	for _, userTag := range opts.UserTags {
		if err := dockerapi.ImageTag(ctx, opts.DepotTag, userTag); err != nil {
			return err
		}
	}

	// PruneChildren is false to preserve the image if no tag was specified.
	rmOpts := types.ImageRemoveOptions{PruneChildren: false}
	_, err = dockerapi.ImageRemove(ctx, opts.DepotTag, rmOpts)
	return err
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
