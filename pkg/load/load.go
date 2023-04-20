package load

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
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
		manifestConfig, err := decodeNodeResponse(architecture, nodeRes)
		if err != nil {
			return err
		}

		// Start the depot CLI hosted registry and socat proxy.
		var registry LocalRegistryProxy
		err = progress.Wrap("preparing to load", pw.Write, func(logger progress.SubLogger) error {
			registry, err = NewLocalRegistryProxy(ctx, manifestConfig, dockerapi, contentClient, logger)
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
		pullOpt := pullOpts[buildRes.Name]
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

type ManifestConfig struct {
	RawManifest []byte
	RawConfig   []byte
}

// We encode the image manifest and image config within the buildkitd Solve response
// because the content may be GCed by the time this load occurs.
func decodeNodeResponse(architecture string, nodeRes depotbuild.DepotNodeResponse) (*ManifestConfig, error) {
	encodedDesc, ok := nodeRes.SolveResponse.ExporterResponse[exptypes.ExporterImageDescriptorKey]
	if !ok {
		return nil, errors.New("missing image descriptor")
	}

	jsonImageDesc, err := base64.StdEncoding.DecodeString(encodedDesc)
	if err != nil {
		return nil, fmt.Errorf("invalid image descriptor: %w", err)
	}

	var imageDesc ocispecs.Descriptor
	if err := json.Unmarshal(jsonImageDesc, &imageDesc); err != nil {
		return nil, fmt.Errorf("invalid image descriptor json: %w", err)
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
				return nil, fmt.Errorf("invalid image index json: %w", err)
			}

			imageManifest, err = chooseBestImageManifest(architecture, &index)
			if err != nil {
				return nil, err
			}
		}
	}

	rawManifest, ok := imageManifest.Annotations["depot.containerimage.manifest"]
	if !ok {
		return nil, errors.New("missing image manifest")
	}

	rawConfig, ok := imageManifest.Annotations["depot.containerimage.config"]
	if !ok {
		return nil, errors.New("missing image config")
	}

	// Decoding both the manifest and config to ensure they are valid.
	var manifest ocispecs.Manifest
	if err := json.Unmarshal([]byte(rawManifest), &manifest); err != nil {
		return nil, fmt.Errorf("invalid image manifest json: %w", err)
	}

	var ii ocispecs.Image
	if err := json.Unmarshal([]byte(rawConfig), &ii); err != nil {
		return nil, fmt.Errorf("invalid image config json: %w", err)
	}
	return &ManifestConfig{RawManifest: []byte(rawManifest), RawConfig: []byte(rawConfig)}, nil
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

type LocalRegistryProxy struct {
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

// NewLocalRegistryProxy creates a local registry proxy that can be used to pull images from
// buildkitd cache.
//
// This also handles docker for desktop issues that prevent the registry from being accessed directly
// by running a proxy container with socat forwarding to the running server.
//
// The running server and proxy container will be cleaned-up when Close() is called.
func NewLocalRegistryProxy(ctx context.Context, manifestConfig *ManifestConfig, dockerapi docker.APIClient, contentClient contentapi.ContentClient, logger progress.SubLogger) (LocalRegistryProxy, error) {
	registryHandler := NewRegistry(contentClient, manifestConfig.RawConfig, manifestConfig.RawManifest, logger)

	ctx, cancel := context.WithCancel(ctx)
	registryPort, err := serveRegistry(ctx, registryHandler)
	if err != nil {
		cancel()
		return LocalRegistryProxy{}, err
	}

	proxyContainerID, proxyExposedPort, err := RunProxyImage(ctx, dockerapi, registryPort)
	if err != nil {
		cancel()
		return LocalRegistryProxy{}, err
	}

	// Wait for the registry and the proxy to be ready.
	dockerAccessibleHost := fmt.Sprintf("localhost:%s", proxyExposedPort)
	var ready bool
	for !ready {
		ready = IsReady(ctx, dockerAccessibleHost)
		if ready {
			break
		}

		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}

	randomImageName := RandImageName()
	// The tag is only for the UX during a pull.  The first line will be "pulling manifest".
	tag := "manifest"
	// Docker is able to pull from the proxyPort on localhost.  The socat proxy
	// forwards to the registry server running on the registryPort.
	imageToPull := fmt.Sprintf("localhost:%s/%s:%s", proxyExposedPort, randomImageName, tag)

	return LocalRegistryProxy{
		ImageToPull:      imageToPull,
		ProxyContainerID: proxyContainerID,
		Cancel:           cancel,
		DockerAPI:        dockerapi,
	}, nil
}

// Close will stop the registry server and remove the proxy container if it was created.
func (l *LocalRegistryProxy) Close(ctx context.Context) error {
	l.Cancel()
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

// The registry can pull images from buildkitd's content store.
// Cancel the context to stop the registry.
func serveRegistry(ctx context.Context, registry *Registry) (int, error) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return 0, err
	}

	server := &http.Server{
		Handler: registry,
	}

	go func() {
		<-ctx.Done()
		_ = server.Shutdown(ctx)
	}()

	go func() {
		_ = server.Serve(listener)
	}()

	return listener.Addr().(*net.TCPAddr).Port, nil
}

// During a download of an image we temporarily store the image with this
// random name to avoid conflicts with any other images.
func RandImageName() string {
	const letterBytes = "abcdefghijklmnopqrstuvwxyz"
	name := make([]byte, 10)
	for i := range name {
		name[i] = letterBytes[rand.Intn(len(letterBytes))]
	}

	return string(name)
}

// IsReady checks if the registry is ready to be used.
func IsReady(ctx context.Context, addr string) bool {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/v2/", nil)
	_, err := http.DefaultClient.Do(req)

	return err == nil
}
