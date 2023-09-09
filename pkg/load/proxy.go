package load

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"sync"
	"time"

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
}

type ProxyConfig struct {
	// Image is the registry proxy image to run.
	Image string

	// Addr is the remote buildkit address (e.g. tcp://192.168.0.1)
	Addr   string
	CACert []byte
	Key    []byte
	Cert   []byte

	// RawManifest is the raw manifest bytes for the single image to serve.
	RawManifest []byte
	// RawConfig is the raw config bytes for the single image to serve.
	RawConfig []byte
}

// Runs a proxy container via the docker API so that the docker daemon can pull from the local depot registry.
// This is specifically to handle docker for desktop running in a VM restricting access to the host network.
// The proxy image runs a registry proxy that connects to the remote depot buildkitd instance.
func RunProxyImage(ctx context.Context, dockerapi docker.APIClient, config *ProxyConfig) (*ProxyContainer, error) {
	if err := PullProxyImage(ctx, dockerapi, config.Image); err != nil {
		return nil, err
	}

	resp, err := dockerapi.ContainerCreate(ctx,
		&container.Config{
			Image: config.Image,
			ExposedPorts: nat.PortSet{
				nat.Port("8888/tcp"): struct{}{},
			},
			Env: []string{
				fmt.Sprintf("CA_CERT=%s", base64.StdEncoding.EncodeToString(config.CACert)),
				fmt.Sprintf("KEY=%s", base64.StdEncoding.EncodeToString(config.Key)),
				fmt.Sprintf("CERT=%s", base64.StdEncoding.EncodeToString(config.Cert)),
				fmt.Sprintf("ADDR=%s", base64.StdEncoding.EncodeToString([]byte(config.Addr))),
				fmt.Sprintf("MANIFEST=%s", base64.StdEncoding.EncodeToString(config.RawManifest)),
				fmt.Sprintf("CONFIG=%s", base64.StdEncoding.EncodeToString(config.RawConfig)),
			},
			Cmd: []string{"registry"},
			Healthcheck: &container.HealthConfig{
				Test:        []string{"CMD", "curl", "-f", "http://localhost:8888/v2"},
				Timeout:     time.Second,
				Interval:    time.Second,
				StartPeriod: 0,
				Retries:     10,
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
		return nil, err
	}

	if err := dockerapi.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return nil, err
	}

	for retries := 0; retries < 10; retries++ {
		inspect, err := dockerapi.ContainerInspect(ctx, resp.ID)
		if err != nil {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			_ = StopProxyContainer(ctx, dockerapi, resp.ID)
			return nil, err
		}

		if inspect.State.Health != nil && inspect.State.Health.Status == "healthy" {
			binds := inspect.NetworkSettings.Ports[nat.Port("8888/tcp")]
			var proxyPortOnHost string
			for _, bind := range binds {
				proxyPortOnHost = bind.HostPort
			}

			return &ProxyContainer{
				ID:   resp.ID,
				Port: proxyPortOnHost,
			}, nil
		}

		time.Sleep(1 * time.Second)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = StopProxyContainer(ctx, dockerapi, resp.ID)
	return nil, fmt.Errorf("timed out waiting for registry to be ready")
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
