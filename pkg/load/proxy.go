package load

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	docker "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

const ProxyImageName = "ghcr.io/depot/helper:1"

// Runs a proxy container via the docker API so that the docker daemon can pull from the local depot registry.
// This is specifically to handle docker for desktop running in a VM restricting access to the host network.
func RunProxyImage(ctx context.Context, dockerapi docker.APIClient, registryPort int) (string, string, error) {
	body, err := dockerapi.ImagePull(ctx, ProxyImageName, types.ImagePullOptions{})
	if err != nil {
		return "", "", err
	}
	defer body.Close()
	_, err = io.Copy(io.Discard, body)
	if err != nil {
		return "", "", err
	}

	resp, err := dockerapi.ContainerCreate(ctx,
		&container.Config{
			Image: ProxyImageName,
			ExposedPorts: nat.PortSet{
				nat.Port("8888/tcp"): struct{}{},
			},
			Cmd: []string{
				"socat",
				"TCP-LISTEN:8888,fork",
				fmt.Sprintf("TCP:host.docker.internal:%d", registryPort),
			},
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
		return "", "", err
	}

	if err := dockerapi.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return "", "", err
	}

	inspect, err := dockerapi.ContainerInspect(ctx, resp.ID)
	if err != nil {
		return "", "", err
	}
	binds := inspect.NetworkSettings.Ports[nat.Port("8888/tcp")]
	var proxyPortOnHost string
	for _, bind := range binds {
		proxyPortOnHost = bind.HostPort
	}

	return resp.ID, proxyPortOnHost, nil
}

// Forcefully stops and removes the proxy container.
func StopProxyContainer(ctx context.Context, dockerapi docker.APIClient, containerID string) error {
	return dockerapi.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
}
