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

	targetPlatforms := map[string]map[string]SBOM{}
	for _, buildRes := range resp {
		targetName := buildRes.Name
		for _, nodeRes := range buildRes.NodeResponses {
			sboms, err := DecodeNodeResponses(nodeRes)
			if err != nil {
				return err
			}

			if sboms == nil {
				continue
			}

			for _, sbom := range sboms {
				platform := strings.ReplaceAll(sbom.Platform, "/", "_")
				if _, ok := targetPlatforms[targetName]; !ok {
					targetPlatforms[targetName] = map[string]SBOM{}
				}
				targetPlatforms[targetName][platform] = sbom
			}
		}
	}

	return writeSBOMs(targetPlatforms, outputDir)
}

func writeSBOMs(targetPlatforms map[string]map[string]SBOM, outputDir string) error {
	numBuildTargets := len(targetPlatforms)
	for targetName, platforms := range targetPlatforms {
		numPlatforms := len(platforms)
		for platform, sbom := range platforms {
			if sbom.Statement == nil {
				continue
			}

			var fileName string
			if numBuildTargets == 1 && numPlatforms == 1 {
				fileName = "sbom.spdx.json"
			} else if numBuildTargets == 1 {
				fileName = fmt.Sprintf("%s.spdx.json", platform)
			} else if numPlatforms == 1 {
				fileName = fmt.Sprintf("%s.spdx.json", targetName)
			} else {
				fileName = fmt.Sprintf("%s_%s.spdx.json", targetName, platform)
			}

			pathName := path.Join(outputDir, fileName)
			err := os.WriteFile(pathName, sbom.Statement, 0644)
			if err != nil {
				return err
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

// This is a custom marshaler to prevent conversion of JSON statement into base64.
func (s *SBOM) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		Statement json.RawMessage `json:"statement"`
		Platform  string          `json:"platform"`
		Image     *ImageSBOM      `json:"image,omitempty"`
	}{
		Statement: json.RawMessage(s.Statement),
		Platform:  s.Platform,
		Image:     s.Image,
	})
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
