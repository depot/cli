package load

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/buildx/util/progress"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/registry"
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
	dockerPullOpts := types.ImagePullOptions{}
	if opts.Username != nil && opts.Password != nil {
		authConfig := registry.AuthConfig{
			Username: *opts.Username,
			Password: *opts.Password,
		}
		if opts.ServerAddress != nil {
			authConfig.ServerAddress = *opts.ServerAddress
		}
		buf, err := json.Marshal(authConfig)
		if err != nil {
			return err
		}
		encodedAuth := base64.URLEncoding.EncodeToString(buf)
		dockerPullOpts.RegistryAuth = encodedAuth
	}
	if opts.Platform != nil {
		dockerPullOpts.Platform = *opts.Platform
	}

	responseBody, err := dockerapi.ImagePull(ctx, imageName, dockerPullOpts)
	if err != nil {
		return err
	}
	defer responseBody.Close()

	if opts.Quiet {
		_, err := io.Copy(io.Discard, responseBody)
		if err != nil {
			return err
		}
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

	if !opts.KeepImage {
		// PruneChildren is false to preserve the image if no tag was specified.
		rmOpts := types.ImageRemoveOptions{PruneChildren: false}
		_, err = dockerapi.ImageRemove(ctx, imageName, rmOpts)
		if err != nil {
			return err
		}
	}

	return nil
}

type Status int

const (
	Unknown Status = iota
	PullingFrom
	PullingFSLayer
	Waiting
	AlreadyExists
	Downloading
	Verifying
	DownloadComplete
	Extracting
	PullComplete
)

func NewStatus(s string) Status {
	switch s {
	case "Pulling fs layer":
		return PullingFSLayer
	case "Waiting":
		return Waiting
	case "Already exists":
		return AlreadyExists
	case "Downloading":
		return Downloading
	case "Verifying Checksum":
		return Verifying
	case "Download complete":
		return DownloadComplete
	case "Extracting":
		return Extracting
	case "Pull complete":
		return PullComplete
	default:
		if strings.HasPrefix(s, "Pulling from ") {
			return PullingFrom
		}
		return Unknown
	}
}

func (s Status) String() string {
	switch s {
	case Unknown:
		return "unknown"
	case PullingFrom:
		return "pulling from"
	case PullingFSLayer:
		return "pulling fs layer"
	case Waiting:
		return "waiting"
	case AlreadyExists:
		return "already exists"
	case Downloading:
		return "downloading"
	case Verifying:
		return "verifying"
	case DownloadComplete:
		return "download complete"
	case Extracting:
		return "extracting"
	case PullComplete:
		return "pull complete"
	default:
		return "unknown"
	}
}

type PullProgress struct {
	Status Status
	Vtx    *client.VertexStatus
}

func printPull(_ context.Context, rc io.Reader, l progress.SubLogger) error {
	started := map[string]PullProgress{}

	defer func() {
		for _, st := range started {
			if st.Vtx.Completed == nil {
				now := time.Now()
				st.Vtx.Completed = &now
				st.Vtx.Current = st.Vtx.Total
				l.SetStatus(st.Vtx)
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

		status := NewStatus(jm.Status)
		// The Pulling fs, pulling from, and waiting don't seem to be too useful to display.
		if status == PullingFSLayer || status == PullingFrom || status == Waiting {
			continue
		}

		// Assume that any unknown status is an error message to be logged.
		if status == Unknown {
			l.Log(0, []byte(jm.Status+"\n"))
			continue
		}

		id := status.String() + " " + jm.ID

		// The first "layer" is the tag.  We've specially tagged the image to be manifest so the UX looks better.
		if jm.ID == "manifest" {
			id = "pulling manifest"
		}
		st, ok := started[jm.ID]
		if !ok {
			if jm.Progress == nil || status == DownloadComplete || status == PullComplete {
				continue
			}

			now := time.Now()
			st = PullProgress{
				Status: status,
				Vtx: &client.VertexStatus{
					ID:      id,
					Started: &now,
				},
			}
			started[jm.ID] = st
		}

		st.Vtx.Timestamp = time.Now()

		// If our new state is further along than the other state, send the older state and update to the new state.
		if st.Status < status {
			now := time.Now()
			st.Vtx.Completed = &now
			st.Vtx.Current = st.Vtx.Total
			l.SetStatus(st.Vtx)

			// We use the "complete" steps to complete the previous step, but not create a new one.
			// The "complete" steps don't contain any other extra information.
			if status == DownloadComplete || status == PullComplete {
				delete(started, jm.ID)
				continue
			}

			st = PullProgress{
				Status: status,
				Vtx: &client.VertexStatus{
					ID:      id,
					Started: &now,
				},
			}
			started[jm.ID] = st
		}

		if jm.Progress != nil {
			st.Vtx.Current = jm.Progress.Current
			st.Vtx.Total = jm.Progress.Total
		}

		// Errors or already exists should complete so that the color changes in the UI.
		if jm.Error != nil || st.Status == AlreadyExists {
			now := time.Now()
			st.Vtx.Completed = &now
		}

		l.SetStatus(st.Vtx)
	}
	return nil
}
