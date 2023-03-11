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

func ShouldLoad(exportLoad bool, opts map[string]build.Options) bool {
	if exportLoad {
		return true
	}

	for _, o := range opts {
		for _, e := range o.Exports {
			if e.Type == "docker" {
				return true
			}
		}
	}
	return false
}

// Options to download from the Depot hosted registry and tag the image with the user provide tag.
type PullOptions struct {
	UserTag            string // Tag used in user input
	DepotTag           string // Tag used in depot hosted registry
	DepotRegistryURL   string // URL of depot hosted registry
	DepotRegistryToken string // Token used to authenticate with depot hosted registry
	Quiet              bool   // No logs plz
}

func PullImages(ctx context.Context, dockerapi docker.APIClient, opts PullOptions, w progress.Writer) error {
	pw := progress.WithPrefix(w, "default", false)

	err := progress.Wrap(fmt.Sprintf("pulling %s", opts.UserTag), pw.Write, func(l progress.SubLogger) error {
		return ImagePullPrivileged(ctx, dockerapi, opts, l)
	})

	if err != nil {
		return err
	}

	progress.Write(pw, fmt.Sprintf("pulled %s", opts.UserTag), func() error { return nil })

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

	// Swap the depot tag with the user-specified tag by adding the user tag
	// and removing the depot one.
	if err := dockerapi.ImageTag(ctx, opts.DepotTag, opts.UserTag); err != nil {
		return err
	}

	_, err = dockerapi.ImageRemove(ctx, opts.DepotTag, types.ImageRemoveOptions{})
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
