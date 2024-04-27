package load

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"

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
		pullOpt := pullOpts[buildRes.Name]

		digest := nodeRes.SolveResponse.ExporterResponse[exptypes.ExporterImageDigestKey]
		if v, ok := nodeRes.SolveResponse.ExporterResponse[exptypes.ExporterImageConfigDigestKey]; ok {
			digest = v
		}
		if digest == "" {
			return errors.New("missing image digest")
		}

		info := struct {
			Address string `json:"address"`
			Cert    string `json:"cert"`
			Key     string `json:"key"`
			CaCert  string `json:"caCert"`
		}{
			Address: nodeRes.Node.DriverOpts["addr"],
			Cert:    base64.StdEncoding.EncodeToString([]byte(nodeRes.Node.DriverOpts["cert"])),
			Key:     base64.StdEncoding.EncodeToString([]byte(nodeRes.Node.DriverOpts["key"])),
			CaCert:  base64.StdEncoding.EncodeToString([]byte(nodeRes.Node.DriverOpts["caCert"])),
		}

		username := "x-info"
		passwordBytes, err := json.Marshal(info)
		if err != nil {
			return fmt.Errorf("failed to marshal info: %w", err)
		}
		password := string(passwordBytes)
		serverAddress := "depot-pull.fly.dev" // TODO: move this to the API

		pullOpt.Username = &username
		pullOpt.Password = &password
		pullOpt.ServerAddress = &serverAddress

		randomImageName := RandImageName()
		tag := "manifest"
		imageToPull := fmt.Sprintf("%s/%s:%s@%s", serverAddress, randomImageName, tag, digest)

		// Pull the image and relabel it with the user specified tags.
		err = PullImages(ctx, dockerapi, imageToPull, pullOpt, pw)
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

// ImageExported is the solve response key added for `depot.export.image.version=2`.
const ImagesExported = "depot/images.exported"

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
