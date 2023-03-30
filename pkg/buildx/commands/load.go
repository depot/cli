package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	"github.com/depot/cli/pkg/buildx/builder"
	docker "github.com/docker/docker/client"
	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type LocalRegistryProxy struct {
	// ImageToPull is the image that should be pulled.
	ImageToPull string
	// Used as the default tag when no tag is specified by the user.
	DefaultDigest digest.Digest
	// ProxyContainerID is the ID of the container that is proxying the registry.
	// If using docker for desktop this will be set.
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
// The running server and proxy container will be cleanedup when Close() is called.
func NewLocalRegistryProxy(ctx context.Context, nodes []builder.Node, containerImageDigest string, dockerapi docker.APIClient) (LocalRegistryProxy, error) {
	contentClient, err := getLocalDriver(ctx, nodes)
	if err != nil {
		return LocalRegistryProxy{}, err
	}

	imageIndex, err := downloadImageIndex(ctx, contentClient, containerImageDigest)
	if err != nil {
		return LocalRegistryProxy{}, err
	}

	registryPort, err := GetFreePort()
	if err != nil {
		return LocalRegistryProxy{}, err
	}

	randomImageName := RandImageName()
	registryHandler := NewRegistry(contentClient, randomImageName.Name, imageIndex)
	// defaultDigest is used way over in the pull code.  It is only used when
	// the user has not specified a tag.
	defaultDigest, err := registryHandler.DefaultDigest()
	if err != nil {
		return LocalRegistryProxy{}, err
	}

	ctx, cancel := context.WithCancel(ctx)
	err = serveRegistry(ctx, registryHandler, registryPort)
	if err != nil {
		cancel()
		return LocalRegistryProxy{}, err
	}

	// Docker for Desktop requires us to run a proxy container to access the registry
	// because it is running in a VM causing us not able to reach localhost.
	//
	// Only localhost is allowed to use http rather than https.
	isDesktop, err := ShouldProxyDockerForDesktop(ctx, dockerapi)
	if err != nil {
		cancel()
		return LocalRegistryProxy{}, err
	}

	var (
		dockerAccessiblePort int
		proxyContainerID     string
	)
	if !isDesktop {
		// When not using docker for desktop we'll assume that docker can access
		// the depot CLI registry port directly.
		dockerAccessiblePort = registryPort
	} else {
		proxyPort, err := GetFreePort()
		if err != nil {
			cancel()
			return LocalRegistryProxy{}, err
		}
		dockerAccessiblePort = proxyPort
		proxyContainerID, err = RunProxyImage(ctx, dockerapi, registryPort, proxyPort)
		if err != nil {
			cancel()
			return LocalRegistryProxy{}, err
		}
	}

	// Wait for the registry and the optional proxy to be ready.
	dockerAccessibleHost := fmt.Sprintf("localhost:%d", dockerAccessiblePort)
	for {
		ready := false

		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
			ready = IsReady(ctx, dockerAccessibleHost)
		}

		if ready {
			break
		}
	}

	// The dockerAccessiblePort is the port is the proxy if docker for desktop or
	// the depot CLI registry port otherwise.
	imageToPull := fmt.Sprintf("localhost:%d/%s:%s", dockerAccessiblePort, randomImageName.Name, randomImageName.Tag)

	return LocalRegistryProxy{
		ImageToPull:      imageToPull,
		DefaultDigest:    defaultDigest,
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
		server.Shutdown(ctx)
	}()

	go func() {
		server.Serve(listener)
	}()

	return nil
}

// This attempts to guess at the os of the local architecture.
func getLocalDriver(ctx context.Context, nodes []builder.Node) (contentapi.ContentClient, error) {
	clients := []*client.Client{}
	var nativeClient *client.Client

	for _, node := range nodes {

		if node.Driver == nil {
			continue
		}

		client, err := node.Driver.Client(ctx)
		if err != nil {
			continue
		}

		if client == nil {
			continue
		}

		platform, ok := node.DriverOpts["platform"]
		if ok && strings.Contains(platform, runtime.GOARCH) {
			nativeClient = client
		} else {
			clients = append(clients, client)
		}
	}

	// Prefer the native architecture client if it exists.
	if nativeClient != nil {
		return nativeClient.ContentClient(), nil
	}

	// Otherwise, just return the first client.
	if len(clients) > 0 {
		return clients[0].ContentClient(), nil
	}

	return nil, nil
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

	var res *contentapi.ReadContentResponse
	// We assume that the entire config will fit into the default buffer (1MB).
	// TODO: Otherwise, we should buffer until EOF.
	res, err = reader.Recv()
	if err != nil {
		return ocispecs.Index{}, err
	}

	var index ocispecs.Index
	if err := json.Unmarshal([]byte(res.Data), &index); err != nil {
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
