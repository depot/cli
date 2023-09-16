package sbom

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"

	depotbuild "github.com/depot/cli/pkg/buildx/build"
)

func Save(outputDir string, resp []depotbuild.DepotBuildResponse) error {
	err := os.MkdirAll(outputDir, 0750)
	if err != nil {
		return err
	}

	numBuilds := len(resp)
	for _, buildRes := range resp {
		buildName := buildRes.Name
		for _, nodeRes := range buildRes.NodeResponses {
			sboms, err := DecodeNodeResponses(nodeRes)
			if err != nil {
				return err
			}

			if sboms == nil {
				continue
			}

			numPlatforms := len(sboms)
			for _, sbom := range sboms {
				platform := strings.ReplaceAll(sbom.Platform, "/", "_")
				var name string
				if numBuilds == 1 && numPlatforms == 1 {
					name = "sbom.spdx.json"
				} else if numBuilds == 1 {
					name = fmt.Sprintf("%s.spdx.json", platform)
				} else if numPlatforms == 1 {
					name = fmt.Sprintf("%s.spdx.json", buildName)
				} else {
					name = fmt.Sprintf("%s_%s.spdx.json", buildName, platform)
				}
				pathName := path.Join(outputDir, name)
				err := os.WriteFile(pathName, sbom.Statement, 0644)
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// SBOMsLabel is the key for the SBOM attestation.
const SBOMsLabel = "depot/sboms"

type SBOM struct {
	// Statement is the spdx.json SBOM scanning output.
	Statement []byte `json:"statement"`
	// Platform is the specific platform that was scanned.
	Platform string `json:"platform"`
	// If an image was created this is the image name and digest of the scanned SBOM.
	Image *ImageSBOM `json:"image"`
}

// ImageSBOM describes an image that is described by an SBOM.
type ImageSBOM struct {
	// Name is the image name and tag.
	Name string `json:"name"`
	// ManifestDigest is the digest of the manifest and can be used
	// to pull the image such as:
	// docker pull goller/xmarks@sha256:6839c1808eab334a9b0f400f119773a0a7d494631c083aef6d3447e3798b544f
	ManifestDigest string `json:"manifest_digest"`
}

// DecodeNodeResponses decodes the SBOMs from the node responses. If the
// response does not have SBOMs, nil is returned.
func DecodeNodeResponses(nodeRes depotbuild.DepotNodeResponse) ([]SBOM, error) {
	encodedSBOMs, ok := nodeRes.SolveResponse.ExporterResponse[SBOMsLabel]
	if !ok {
		return nil, nil
	}
	sboms, err := DecodeSBOMs(encodedSBOMs)
	if err != nil {
		return nil, fmt.Errorf("invalid exported images json: %w", err)
	}

	return sboms, nil
}

func DecodeSBOMs(encodedSBOMs string) ([]SBOM, error) {
	b64 := base64.NewDecoder(base64.StdEncoding, bytes.NewBufferString(encodedSBOMs))
	var sboms []SBOM
	err := json.NewDecoder(b64).Decode(&sboms)
	return sboms, err
}
