package commands

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	docker "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

const ProxyImageName = "ghcr.io/depot/helper:1"

func ShouldProxyDockerForDesktop(ctx context.Context, dockerapi docker.APIClient) (bool, error) {
	version, err := dockerapi.ServerVersion(ctx)
	if err != nil {
		return false, err
	}

	isDesktop := strings.Contains(version.Platform.Name, "Desktop")
	return isDesktop, nil
}

// Runs a proxy container via the docker API so that the docker daemon can pull from the local depot registry.
// This is specifically to handle docker for desktop running in a VM restricting access to the host network.
func RunProxyImage(ctx context.Context, dockerapi docker.APIClient, registryPort, proxyPort int) (string, error) {
	body, err := dockerapi.ImagePull(ctx, ProxyImageName, types.ImagePullOptions{})
	if err != nil {
		return "", err
	}
	defer body.Close()
	_, err = io.Copy(io.Discard, body)
	if err != nil {
		return "", err
	}

	resp, err := dockerapi.ContainerCreate(ctx,
		&container.Config{
			Image: ProxyImageName,
			ExposedPorts: nat.PortSet{
				nat.Port(fmt.Sprintf("%d/tcp", proxyPort)): struct{}{},
			},
			Cmd: []string{
				"socat",
				fmt.Sprintf("TCP-LISTEN:%d,fork", proxyPort),
				fmt.Sprintf("TCP:host.docker.internal:%d", registryPort),
			},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				nat.Port(fmt.Sprintf("%d/tcp", proxyPort)): []nat.PortBinding{{HostPort: fmt.Sprintf("%d", proxyPort)}},
			},
		},
		nil,
		nil,
		fmt.Sprintf("depot-registry-proxy-%d", proxyPort), // unique container name
	)

	if err != nil {
		return "", err
	}

	if err := dockerapi.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return "", err
	}

	return resp.ID, nil
}

// Forcefully stops and removes the proxy container.
func StopProxyContainer(ctx context.Context, dockerapi docker.APIClient, containerID string) error {
	return dockerapi.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
}

// GetFreePort asks the kernel for a free open port that is ready to use.
func GetFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
