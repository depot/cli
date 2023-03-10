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

func PullImages(ctx context.Context, tags []string, dockerCli command.Cli, w progress.Writer) error {
	pw := progress.WithPrefix(w, "default", false)

	for _, tag := range tags {
		opts := PullOptions{Remote: tag}

		err := progress.Wrap(fmt.Sprintf("pulling %s", tag), pw.Write, func(l progress.SubLogger) error {
			return ImagePullPrivileged(ctx, dockerCli, opts, l)
		})

		if err != nil {
			return err
		}

		progress.Write(pw, fmt.Sprintf("pulled %s", tag), func() error { return nil })
	}

	return nil
}

// PullOptions defines what and how to pull
type PullOptions struct {
	Remote string
	Quiet  bool
}

func ImagePullPrivileged(ctx context.Context, cli command.Cli, opts PullOptions, l progress.SubLogger) error {
	ref := opts.Remote

	/*
		authConfig := types.AuthConfig{
			// base64 encoded username and password.
			Auth: "",
			// This is what is place in the Authorization: Bearer token.
			RegistryToken: "howdy",
			ServerAddress: "ecr.us-east-1.amazonaws.com",
		}
	*/

	authConfig := types.AuthConfig{
		Username:      "goller",
		ServerAddress: "https://index.docker.io/v1/",
	}

	encodedAuth, err := command.EncodeAuthToBase64(authConfig)
	if err != nil {
		return err
	}

	options := types.ImagePullOptions{
		RegistryAuth: encodedAuth,
		//Platform:     opts.Platform,
	}

	responseBody, err := cli.Client().ImagePull(ctx, ref, options)
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

	// TODO: errors and correct tag.
	cli.Client().ImageTag(ctx, opts.Remote, "goller:latest")
	cli.Client().ImageRemove(ctx, opts.Remote, types.ImageRemoveOptions{})

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
