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

	depotprogress "github.com/depot/cli/pkg/progress"
	"github.com/docker/buildx/build"
	"github.com/docker/buildx/util/progress"
	docker "github.com/docker/docker/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

func DepotFastLoad(ctx context.Context, dockerapi docker.APIClient, resp []build.DepotBuildResponse, pullOpts map[string]PullOptions, printer *depotprogress.Progress) error {
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
		pullOpt := pullOpts[buildRes.Name]

		architecture := nodeRes.Node.DriverOpts["platform"]
		manifest, config, err := decodeNodeResponse(architecture, nodeRes)
		if err != nil {
			return err
		}
		proxyOpts := &ProxyConfig{
			RawManifest: manifest,
			RawConfig:   config,
			Addr:        nodeRes.Node.DriverOpts["addr"],
			CACert:      []byte(nodeRes.Node.DriverOpts["caCert"]),
			Key:         []byte(nodeRes.Node.DriverOpts["key"]),
			Cert:        []byte(nodeRes.Node.DriverOpts["cert"]),
		}

		// Start the depot registry proxy.
		var registry *RegistryProxy
		err = progress.Wrap("preparing to load", pw.Write, func(logger progress.SubLogger) error {
			registry, err = NewRegistryProxy(ctx, proxyOpts, dockerapi)
			if err != nil {
				err = logger.Wrap(fmt.Sprintf("[registry] unable to start: %s", err), func() error { return err })
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
func chooseNodeResponse(nodeResponses []build.DepotNodeResponse) build.DepotNodeResponse {
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

// ImageExported is the solve response key added for `depot.export.image.version=2`.
const ImagesExported = "depot/images.exported"

func decodeNodeResponse(architecture string, nodeRes build.DepotNodeResponse) (rawManifest, rawConfig []byte, err error) {
	if _, err := EncodedExportedImages(nodeRes.SolveResponse.ExporterResponse); err == nil {
		return decodeNodeResponseV2(architecture, nodeRes)
	}

	// Needed until all depot builds and CLI versions are updated.
	return decodeNodeResponseV1(architecture, nodeRes)
}

func decodeNodeResponseV2(architecture string, nodeRes build.DepotNodeResponse) (rawManifest, rawConfig []byte, err error) {
	encodedExportedImages, err := EncodedExportedImages(nodeRes.SolveResponse.ExporterResponse)
	if err != nil {
		return nil, nil, err
	}

	exportedImages, _, imageConfigs, err := DecodeExportImages(encodedExportedImages)
	if err != nil {
		return nil, nil, err
	}

	idx, err := chooseBestImageManifestV2(architecture, imageConfigs)
	if err != nil {
		return nil, nil, err
	}

	return exportedImages[idx].Manifest, exportedImages[idx].Config, nil
}

// EncodedExportedImages returns the encoded exported images from the solve response.
// This uses the `depot.export.image.version=2` format.
func EncodedExportedImages(exporterResponse map[string]string) (string, error) {
	encodedExportedImages, ok := exporterResponse[ImagesExported]
	if !ok {
		return "", errors.New("missing image export response")
	}
	return encodedExportedImages, nil
}

// RawExportedImage is the JSON-encoded image manifest and config used loading the image.
type RawExportedImage struct {
	// JSON-encoded ocispecs.Manifest.
	// This is double encoded as buildkit has extra fields when used as a docker schema.
	// This matters as the digest is calculated including all those extra fields.
	Manifest []byte `json:"manifest"`
	// JSON-encoded ocispecs.Image.
	// Double encoded for the same reason.
	Config []byte `json:"config"`
}

// DecodeExportImages decodes the exported images from the solve response.
// The solve response is encoded with a bunch of JSON/b64 encoding to attempt
// to pass a variety of data structures to the CLI.
func DecodeExportImages(encodedExportedImages string) ([]RawExportedImage, []ocispecs.Manifest, []ocispecs.Image, error) {
	jsonExportedImages, err := base64.StdEncoding.DecodeString(encodedExportedImages)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid exported images encoding: %w", err)
	}

	var exportedImages []RawExportedImage
	if err := json.Unmarshal(jsonExportedImages, &exportedImages); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid exported images json: %w", err)
	}

	// Potentially multiple platforms were built, so we need to find the
	// manifest and config for the platform that matches the depot CLI host.
	manifests := make([]ocispecs.Manifest, len(exportedImages))
	imageConfigs := make([]ocispecs.Image, len(exportedImages))
	for i := range exportedImages {
		var manifest ocispecs.Manifest
		if err := json.Unmarshal(exportedImages[i].Manifest, &manifest); err != nil {
			return nil, nil, nil, fmt.Errorf("invalid image manifest json: %w", err)
		}
		manifests[i] = manifest

		var image ocispecs.Image
		if err := json.Unmarshal(exportedImages[i].Config, &image); err != nil {
			return nil, nil, nil, fmt.Errorf("invalid image config json: %w", err)
		}
		imageConfigs[i] = image
	}

	return exportedImages, manifests, imageConfigs, nil
}

// We encode the image manifest and image config within the buildkitd Solve response
// because the content may be GCed by the time this load occurs.
func decodeNodeResponseV1(architecture string, nodeRes build.DepotNodeResponse) (rawManifest, rawConfig []byte, err error) {
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

type RegistryProxy struct {
	// ImageToPull is the image that should be pulled.
	ImageToPull string
	// ProxyContainerID is the ID of the container that is proxying the registry.
	// Make sure to remove this container when finished.
	ProxyContainerID string

	// Used to stop and remove the proxy container.
	DockerAPI docker.APIClient
}

// NewRegistryProxy creates a registry proxy that can be used to pull images from
// buildkitd cache.
//
// This also handles docker for desktop issues that prevent the registry from being
// accessed directly because the proxy is accessible by the docker daemon.
// The proxy registry translates pull requests into requests to containerd via mTLS.
//
// The running server and proxy container will be cleaned-up when Close() is called.
func NewRegistryProxy(ctx context.Context, config *ProxyConfig, dockerapi docker.APIClient) (*RegistryProxy, error) {
	proxyContainer, err := RunProxyImage(ctx, dockerapi, config)
	if err != nil {
		return nil, err
	}

	randomImageName := RandImageName()
	// The tag is only for the UX during a pull.  The first line will be "pulling manifest".
	tag := "manifest"
	// Docker is able to pull from the proxyPort on localhost.  The proxy
	// forwards registry requests to buildkitd via mTLS.
	imageToPull := fmt.Sprintf("localhost:%s/%s:%s", proxyContainer.Port, randomImageName, tag)

	registryProxy := &RegistryProxy{
		ImageToPull:      imageToPull,
		ProxyContainerID: proxyContainer.ID,
		DockerAPI:        dockerapi,
	}

	return registryProxy, nil
}

// Close will stop and remove the registry proxy container if it was created.
func (l *RegistryProxy) Close(ctx context.Context) error {
	return StopProxyContainer(ctx, l.DockerAPI, l.ProxyContainerID)
}

// Prefer architecture, otherwise, take first available index.
func chooseBestImageManifestV2(architecture string, imageConfigs []ocispecs.Image) (int, error) {
	archIdx := map[string]int{}
	for i, imageConfig := range imageConfigs {
		if imageConfig.Architecture == "unknown" {
			continue
		}

		archIdx[imageConfig.Architecture] = i
	}

	// Prefer the architecture of the depot CLI host, otherwise, take first available.
	if idx, ok := archIdx[architecture]; ok {
		return idx, nil
	}

	for _, idx := range archIdx {
		return idx, nil
	}

	return 0, errors.New("no manifests found")
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
