package load

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ImagesExported is the solve response key added for `depot.export.image.version=2`.
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
