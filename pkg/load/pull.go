package load

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/buildx/util/progress"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/moby/buildkit/client"
)

// PullImages calls the local docker API to pull the image.
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
