package load

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
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
		contentClient, err := contentClient(ctx, nodeRes)
		if err != nil {
			return err
		}

		architecture := nodeRes.Node.DriverOpts["platform"]
		manifest, config, err := decodeNodeResponse(architecture, nodeRes)
		if err != nil {
			return err
		}
		pullOpt := pullOpts[buildRes.Name]
		proxyOpts := &ProxyOpts{
			RawManifest: manifest,
			RawConfig:   config,
			ProxyImage:  pullOpt.ProxyImage,
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

// We encode the image manifest and image config within the buildkitd Solve response
// because the content may be GCed by the time this load occurs.
func decodeNodeResponse(architecture string, nodeRes depotbuild.DepotNodeResponse) (rawManifest, rawConfig []byte, err error) {
	encodedDesc, ok := nodeRes.SolveResponse.ExporterResponse[exptypes.ExporterImageDescriptorKey]
	if !ok {
		return nil, nil, errors.New("missing image descriptor")
	}

	jsonImageDesc, err := base64.StdEncoding.DecodeString(encodedDesc)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid image descriptor: %w", err)
	}

	var imageDesc ocispecs.Descriptor
	if err := json.Unmarshal(jsonImageDesc, &imageDesc); err != nil {
		return nil, nil, fmt.Errorf("invalid image descriptor json: %w", err)
	}

	var imageManifest ocispecs.Descriptor = imageDesc
	{
		// These checks handle situations where the image does and does not have attestations.
		// If there are no attestations, then the imageDesc contains the manifest and config.
		// Otherwise the imageDesc's `depot.containerimage.index` will contain the manifest and config.

		encodedIndex, ok := imageDesc.Annotations["depot.containerimage.index"]
		if ok {
			var index ocispecs.Index
			if err := json.Unmarshal([]byte(encodedIndex), &index); err != nil {
				return nil, nil, fmt.Errorf("invalid image index json: %w", err)
			}

			imageManifest, err = chooseBestImageManifest(architecture, &index)
			if err != nil {
				return nil, nil, err
			}
		}
	}

	m, ok := imageManifest.Annotations["depot.containerimage.manifest"]
	if !ok {
		return nil, nil, errors.New("missing image manifest")
	}
	rawManifest = []byte(m)

	c, ok := imageManifest.Annotations["depot.containerimage.config"]
	if !ok {
		return nil, nil, errors.New("missing image config")
	}
	rawConfig = []byte(c)

	// Decoding both the manifest and config to ensure they are valid.
	var manifest ocispecs.Manifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		return nil, nil, fmt.Errorf("invalid image manifest json: %w", err)
	}

	var image ocispecs.Image
	if err := json.Unmarshal(rawConfig, &image); err != nil {
		return nil, nil, fmt.Errorf("invalid image config json: %w", err)
	}
	return rawManifest, rawConfig, nil
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

// Prefer architecture, otherwise, take first available.
func chooseBestImageManifest(architecture string, index *ocispecs.Index) (ocispecs.Descriptor, error) {
	archDescriptors := map[string]ocispecs.Descriptor{}
	for _, manifest := range index.Manifests {
		if manifest.Platform == nil {
			continue
		}

		if manifest.Platform.Architecture == "unknown" {
			continue
		}

		archDescriptors[manifest.Platform.Architecture] = manifest
	}

	// Prefer the architecture of the depot CLI host, otherwise, take first available.
	if descriptor, ok := archDescriptors[architecture]; ok {
		return descriptor, nil
	}

	for _, descriptor := range archDescriptors {
		return descriptor, nil
	}

	return ocispecs.Descriptor{}, errors.New("no manifests found")
}
