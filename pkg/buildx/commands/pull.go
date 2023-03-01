package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/buildx/build"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/trust"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/api/types"
	registrytypes "github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/docker/registry"
	"github.com/moby/buildkit/client"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
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

func PullImages(ctx context.Context, tags []string, platforms []v1.Platform, dockerCli command.Cli, w progress.Writer) error {
	pw := progress.WithPrefix(w, "default", false)

	for _, tag := range tags {
		for _, p := range platforms {
			platform := fmt.Sprintf("%s/%s", p.OS, p.Architecture)
			opts := PullOptions{Remote: tag, Platform: platform}

			var image string
			if err := progress.Wrap(fmt.Sprintf("pulling %s %s", tag, platform), pw.Write, func(l progress.SubLogger) error {
				var err error
				image, err = ImagePullPrivileged(ctx, dockerCli, opts, l)
				if err != nil {
					return err
				}

				return nil
			}); err != nil {
				return err
			}

			progress.Write(pw, fmt.Sprintf("pulled %s", image), func() error { return nil })
		}
	}

	return nil
}

// PullOptions defines what and how to pull
type PullOptions struct {
	Remote    string
	All       bool
	Platform  string
	Quiet     bool
	Untrusted bool
}

func ImagePullPrivileged(ctx context.Context, cli command.Cli, opts PullOptions, l progress.SubLogger) (string, error) {
	distributionRef, err := reference.ParseNormalizedNamed(opts.Remote)
	switch {
	case err != nil:
		return "", err
	case opts.All && !reference.IsNameOnly(distributionRef):
		return "", errors.New("tag can't be used with --all-tags/-a")
	case !opts.All && reference.IsNameOnly(distributionRef):
		distributionRef = reference.TagNameOnly(distributionRef)
		if tagged, ok := distributionRef.(reference.Tagged); ok && !opts.Quiet {
			fmt.Fprintf(cli.Out(), "Using default tag: %s\n", tagged.Tag())
		}
	}

	imgRefAndAuth, err := trust.GetImageReferencesAndAuth(ctx, nil, AuthResolver(cli), distributionRef.String())
	if err != nil {
		return "", err
	}

	ref := reference.FamiliarString(imgRefAndAuth.Reference())

	encodedAuth, err := command.EncodeAuthToBase64(*imgRefAndAuth.AuthConfig())
	if err != nil {
		return "", err
	}

	index := imgRefAndAuth.RepoInfo().Index

	options := types.ImagePullOptions{
		RegistryAuth:  encodedAuth,
		PrivilegeFunc: RegistryAuthenticationPrivilegedFunc(index),
		All:           opts.All,
		Platform:      opts.Platform,
	}

	responseBody, err := cli.Client().ImagePull(ctx, ref, options)
	if err != nil {
		return "", err
	}
	defer responseBody.Close()

	if opts.Quiet {
		_, err := io.Copy(io.Discard, responseBody)
		return "", err
	} else {
		if err := printPull(ctx, responseBody, l); err != nil {
			return "", err
		}
	}

	// TODO: errors and correct tag.
	cli.Client().ImageTag(ctx, opts.Remote, "goller:latest")
	cli.Client().ImageRemove(ctx, opts.Remote, types.ImageRemoveOptions{})

	return imgRefAndAuth.Reference().String(), nil
}

// RegistryAuthenticationPrivilegedFunc returns a RequestPrivilegeFunc from the specified registry index info
// for the given command.
func RegistryAuthenticationPrivilegedFunc(index *registrytypes.IndexInfo) types.RequestPrivilegeFunc {
	return func() (string, error) {
		indexServer := registry.GetAuthConfigKey(index)
		isDefaultRegistry := indexServer == registry.IndexServer

		if !isDefaultRegistry {
			indexServer = registry.ConvertToHostname(indexServer)
		}

		authConfig := types.AuthConfig{
			ServerAddress: indexServer,
		}

		return command.EncodeAuthToBase64(authConfig)
	}
}

// AuthResolver returns an auth resolver function from a command.Cli
func AuthResolver(cli command.Cli) func(ctx context.Context, index *registrytypes.IndexInfo) types.AuthConfig {
	return func(ctx context.Context, index *registrytypes.IndexInfo) types.AuthConfig {
		return command.ResolveAuthConfig(ctx, cli, index)
	}
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
