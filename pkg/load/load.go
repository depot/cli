package load

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	depotbuild "github.com/depot/cli/pkg/buildx/build"
	depotprogress "github.com/depot/cli/pkg/progress"
	"github.com/docker/buildx/util/progress"
	docker "github.com/docker/docker/client"
)

func DepotFastLoad(ctx context.Context, dockerapi docker.APIClient, resp []depotbuild.DepotBuildResponse, pullOpts map[string]PullOptions, printer *depotprogress.Progress) error {
	if len(resp) == 0 {
		return nil
	}

	if len(pullOpts) == 0 {
		return nil
	}

	for _, buildRes := range resp {
		pw := progress.WithPrefix(printer, buildRes.Name, len(pullOpts) > 1)
		// Pick the best node to pull from by checking against local architecture.
		nodeRes := chooseNodeResponse(buildRes.NodeResponses)

		architecture := nodeRes.Node.DriverOpts["platform"]
		best, err := chooseBestImageManifest(architecture, nodeRes)
		if err != nil {
			return err
		}

		pullOpt := pullOpts[buildRes.Name]
		proxyOpts := &ProxyOpts{
			RawManifest: []byte(nodeRes.ManifestConfigs[best].RawManifest),
			RawConfig:   []byte(nodeRes.ManifestConfigs[best].RawImageConfig),
			ProxyImage:  pullOpt.ProxyImage,
		}

		contentClient, err := contentClient(ctx, nodeRes)
		if err != nil {
			return err
		}

		// Start the depot registry proxy.
		var registry *RegistryProxy
		err = progress.Wrap("preparing to load", pw.Write, func(logger progress.SubLogger) error {
			registry, err = NewRegistryProxy(ctx, proxyOpts, dockerapi, contentClient, logger)
			if err != nil {
				err = logger.Wrap(fmt.Sprintf("[registry] unable to start %s", err), func() error { return err })
			}
			return err
		})
		if err != nil {
			return err
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			registry.Close(ctx)
			cancel()
		}()

		// Pull the image and relabel it with the user specified tags.
		err = PullImages(ctx, dockerapi, registry.ImageToPull, pullOpt, pw)
		if err != nil {
			return fmt.Errorf("failed to pull image: %w", err)
		}
	}

	return nil
}

// For now if there is a multi-platform build we try to only download the
// architecture of the depot CLI host.  If there is not a node with the same
// architecture as the  depot CLI host, we take the first node in the list.
func chooseNodeResponse(nodeResponses []depotbuild.DepotNodeResponse) depotbuild.DepotNodeResponse {
	var nodeIdx int
	for i, nodeResponse := range nodeResponses {
		platform, ok := nodeResponse.Node.DriverOpts["platform"]
		if ok && strings.Contains(platform, runtime.GOARCH) {
			nodeIdx = i
			break
		}
	}

	return nodeResponses[nodeIdx]
}

type ProxyOpts struct {
	RawManifest []byte
	RawConfig   []byte
	ProxyImage  string
}

func contentClient(ctx context.Context, nodeResponse depotbuild.DepotNodeResponse) (contentapi.ContentClient, error) {
	if nodeResponse.Node.Driver == nil {
		return nil, fmt.Errorf("node %s does not have a driver", nodeResponse.Node.Name)
	}

	client, err := nodeResponse.Node.Driver.Client(ctx)
	if err != nil {
		return nil, err
	}

	if client == nil {
		return nil, fmt.Errorf("node %s does not have a client", nodeResponse.Node.Name)
	}

	return client.ContentClient(), nil
}

type RegistryProxy struct {
	// ImageToPull is the image that should be pulled.
	ImageToPull string
	// ProxyContainerID is the ID of the container that is proxying the registry.
	// Make sure to remove this container when finished.
	ProxyContainerID string

	// Cancel is the cancel function for the registry server.
	Cancel context.CancelFunc

	// Used to stop and remove the proxy container.
	DockerAPI docker.APIClient
}

// NewRegistryProxy creates a registry proxy that can be used to pull images from
// buildkitd cache.
//
// This also handles docker for desktop issues that prevent the registry from being
// accessed directly because the proxy is accessible by the docker daemon.
// The proxy registry translates pull requests into a custom protocol over
// stdin and stdout.  We use this proprietary protocol as the Docker daemon itself
// my be remote and the only way to communicate with remote daemons is over `attach`.
//
// The running server and proxy container will be cleaned-up when Close() is called.
func NewRegistryProxy(ctx context.Context, opts *ProxyOpts, dockerapi docker.APIClient, contentClient contentapi.ContentClient, logger progress.SubLogger) (*RegistryProxy, error) {
	ctx, cancel := context.WithCancel(ctx)
	proxyContainer, err := RunProxyImage(ctx, dockerapi, opts.ProxyImage, opts.RawManifest, opts.RawConfig)
	if err != nil {
		cancel()
		return nil, err
	}

	transport := NewTransport(proxyContainer.Conn)
	go func() {
		// Canceling ctx will stop the transport.
		_ = transport.Run(ctx, contentClient)
	}()

	randomImageName := RandImageName()
	// The tag is only for the UX during a pull.  The first line will be "pulling manifest".
	tag := "manifest"
	// Docker is able to pull from the proxyPort on localhost.  The proxy
	// forwards registry requests to the Transport over docker attach's stdin and stdout.
	imageToPull := fmt.Sprintf("localhost:%s/%s:%s", proxyContainer.Port, randomImageName, tag)

	return &RegistryProxy{
		ImageToPull:      imageToPull,
		ProxyContainerID: proxyContainer.ID,
		Cancel:           cancel,
		DockerAPI:        dockerapi,
	}, nil
}

// Close will stop the registry server and remove the proxy container if it was created.
func (l *RegistryProxy) Close(ctx context.Context) error {
	l.Cancel() // This stops the serial transport.
	return StopProxyContainer(ctx, l.DockerAPI, l.ProxyContainerID)
}

// Pick the best architecture from the attestation index if it exists or the zeroth manifest.
func chooseBestImageManifest(architecture string, nodeRes depotbuild.DepotNodeResponse) (int, error) {
	var bestManifest int

	if nodeRes.AttestationIndex != nil {
		archDescriptors := map[string]int{}
		for i, manifest := range nodeRes.AttestationIndex.Manifests {
			if manifest.Platform == nil {
				continue
			}

			if manifest.Platform.Architecture == "unknown" {
				continue
			}

			archDescriptors[manifest.Platform.Architecture] = i
		}

		// Prefer the architecture of the depot CLI host, otherwise, take first available.
		if i, ok := archDescriptors[architecture]; ok {
			bestManifest = i
		} else {
			for _, i := range archDescriptors {
				bestManifest = i
				break
			}
		}
	}

	if bestManifest >= len(nodeRes.ManifestConfigs) {
		return -1, errors.New("response does not contain a manifest")
	}

	return bestManifest, nil
}
