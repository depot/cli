package load

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	docker "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

const DefaultProxyImageName = "ghcr.io/depot/helper:3.0.0"

type ProxyContainer struct {
	ID   string
	Port string
	Conn net.Conn
}

// Runs a proxy container via the docker API so that the docker daemon can pull from the local depot registry.
// This is specifically to handle docker for desktop running in a VM restricting access to the host network.
func RunProxyImage(ctx context.Context, dockerapi docker.APIClient, proxyImage string, rawManifest, rawConfig []byte) (*ProxyContainer, error) {
	if err := PullProxyImage(ctx, dockerapi, proxyImage); err != nil {
		return nil, err
	}

	resp, err := dockerapi.ContainerCreate(ctx,
		&container.Config{
			Image: proxyImage,
			ExposedPorts: nat.PortSet{
				nat.Port("8888/tcp"): struct{}{},
			},
			AttachStdin:  true,
			AttachStdout: true,
			OpenStdin:    true,
			StdinOnce:    true,
			Env: []string{
				fmt.Sprintf("MANIFEST=%s", base64.StdEncoding.EncodeToString(rawManifest)),
				fmt.Sprintf("CONFIG=%s", base64.StdEncoding.EncodeToString(rawConfig)),
			},
			Cmd: []string{"/srv/helper"},
		},
		&container.HostConfig{
			PublishAllPorts: true,
			// This is the trick to make sure that the proxy container can
			// access the host network in a cross platform way.
			ExtraHosts: []string{"host.docker.internal:host-gateway"},
		},
		nil,
		nil,
		fmt.Sprintf("depot-registry-proxy-%s", RandImageName()), // unique container name
	)

	if err != nil {
		return nil, err
	}

	attach, err := dockerapi.ContainerAttach(ctx, resp.ID, types.ContainerAttachOptions{Stdin: true, Stdout: true, Stderr: true, Logs: true, Stream: true})
	if err != nil {
		return nil, err
	}

	if err := dockerapi.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return nil, err
	}

	inspect, err := dockerapi.ContainerInspect(ctx, resp.ID)
	if err != nil {
		_ = StopProxyContainer(ctx, dockerapi, resp.ID)
		return nil, err
	}
	binds := inspect.NetworkSettings.Ports[nat.Port("8888/tcp")]
	var proxyPortOnHost string
	for _, bind := range binds {
		proxyPortOnHost = bind.HostPort
	}

	return &ProxyContainer{
		ID:   resp.ID,
		Port: proxyPortOnHost,
		Conn: attach.Conn,
	}, nil
}

var (
	downloadedProxyImage  sync.Once
	downloadProxyImageErr error
)

// PullProxyImage will pull the proxy image into docker.
// This is done once per process as a performance optimization.
// Additionally, if the proxy image is already present, this will not pull the image.
func PullProxyImage(ctx context.Context, dockerapi docker.APIClient, imageName string) error {
	downloadedProxyImage.Do(func() {
		// Check if image already has been downloaded.
		images, err := dockerapi.ImageList(ctx, types.ImageListOptions{
			Filters: filters.NewArgs(filters.Arg("reference", imageName)),
		})

		// Any error or no matching images means we need to pull the image.
		// The goal is to save about a second or two of startup time.
		if err != nil || len(images) == 0 {
			var body io.ReadCloser
			body, downloadProxyImageErr = dockerapi.ImagePull(ctx, imageName, types.ImagePullOptions{})
			if downloadProxyImageErr != nil {
				return
			}
			defer func() { _ = body.Close() }()
			_, downloadProxyImageErr = io.Copy(io.Discard, body)
			return
		}
	})

	return downloadProxyImageErr
}

// Forcefully stops and removes the proxy container.
func StopProxyContainer(ctx context.Context, dockerapi docker.APIClient, containerID string) error {
	return dockerapi.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
}
