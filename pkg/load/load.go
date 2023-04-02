package load

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	depotbuild "github.com/depot/cli/pkg/build"
	depotprogress "github.com/depot/cli/pkg/progress"
	"github.com/docker/buildx/util/progress"
	docker "github.com/docker/docker/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/opencontainers/go-digest"
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
		containerImageDigest := nodeRes.SolveResponse.ExporterResponse[exptypes.ExporterImageDigestKey]

		// Start the depot CLI hosted registry and socat proxy.
		var registry LocalRegistryProxy
		err = progress.Wrap("preparing to load", pw.Write, func(logger progress.SubLogger) error {
			registry, err = NewLocalRegistryProxy(ctx, architecture, containerImageDigest, dockerapi, contentClient, logger)
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
func NewLocalRegistryProxy(ctx context.Context, architecture string, containerImageDigest string, dockerapi docker.APIClient, contentClient contentapi.ContentClient, logger progress.SubLogger) (LocalRegistryProxy, error) {
	imageIndex, err := downloadImageIndex(ctx, contentClient, containerImageDigest)
	if err != nil {
		return LocalRegistryProxy{}, err
	}

	manifestConfig, err := chooseBestImageManifest(architecture, imageIndex)
	if err != nil {
		return LocalRegistryProxy{}, err
	}
	randomImageName := RandImageName()

	registryHandler := NewRegistry(contentClient, manifestConfig, logger)
	registryPort, err := GetFreePort()
	if err != nil {
		return LocalRegistryProxy{}, err
	}

	ctx, cancel := context.WithCancel(ctx)
	err = serveRegistry(ctx, registryHandler, registryPort)
	if err != nil {
		cancel()
		return LocalRegistryProxy{}, err
	}

	proxyPort, err := GetFreePort()
	if err != nil {
		cancel()
		return LocalRegistryProxy{}, err
	}
	proxyContainerID, err := RunProxyImage(ctx, dockerapi, registryPort, proxyPort)
	if err != nil {
		cancel()
		return LocalRegistryProxy{}, err
	}

	// Wait for the registry and the optional proxy to be ready.
	dockerAccessibleHost := fmt.Sprintf("localhost:%d", proxyPort)
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

	// The dockerAccessiblePort is the port is the proxy if docker for desktop or
	// the depot CLI registry port otherwise.
	imageToPull := fmt.Sprintf("localhost:%d/%s:%s", proxyPort, randomImageName.Name, randomImageName.Tag)

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
func chooseBestImageManifest(architecture string, index ocispecs.Index) (ocispecs.Descriptor, error) {
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
func serveRegistry(ctx context.Context, registry *Registry, registryPort int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", registryPort))
	if err != nil {
		return err
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

	return nil
}

// downloadImageIndex downloads the config file from the image that was just built.
// This is used to know get the manifest and the rest of the image content.
func downloadImageIndex(ctx context.Context, client contentapi.ContentClient, containerImageDigest string) (ocispecs.Index, error) {
	req := &contentapi.ReadContentRequest{
		Digest: digest.Digest(containerImageDigest),
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := client.Read(ctx, req)
	if err != nil {
		return ocispecs.Index{}, err
	}

	octets := make([]byte, 0, 1024*1024)
	for {
		res, err := reader.Recv()
		if err != nil {
			break
		}
		octets = append(octets, res.Data...)
	}

	if err != nil && !errors.Is(err, io.EOF) {
		return ocispecs.Index{}, err
	}

	if len(octets) == 0 {
		return ocispecs.Index{}, errors.New("image digest not found")
	}

	var index ocispecs.Index
	if err := json.Unmarshal(octets, &index); err != nil {
		return ocispecs.Index{}, err
	}

	return index, nil
}

type ImageName struct {
	Name string
	Tag  string
}

// During a download of an image we temporarily storage the image with this
// random name to avoid conflicts with any other images.
func RandImageName() ImageName {
	const letterBytes = "abcdefghijklmnopqrstuvwxyz"
	name := make([]byte, 10)
	for i := range name {
		name[i] = letterBytes[rand.Intn(len(letterBytes))]
	}

	tag := make([]byte, 5)
	for i := range tag {
		tag[i] = letterBytes[rand.Intn(len(letterBytes))]
	}

	return ImageName{
		Name: string(name),
		Tag:  string(tag),
	}
}

// IsReady checks if the registry is ready to be used.
func IsReady(ctx context.Context, addr string) bool {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/v2/", nil)
	_, err := http.DefaultClient.Do(req)

	return err == nil
}
