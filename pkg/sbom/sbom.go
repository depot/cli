package sbom

import (
	"compress/gzip"
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

			numPlatforms := len(sboms.PlatformSBOMs)
			for platform, sbom := range sboms.PlatformSBOMs {
				platform = strings.ReplaceAll(platform, "/", "_")
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
				err := os.WriteFile(pathName, sbom, 0644)
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
	// StableDigests are the stable digests of the layer that was scanned.
	// I'm not sure when there are two digests.
	StableDigests []string `json:"stable_digests"`
	// SBOM per platform. Format is spdx.json.
	PlatformSBOMs map[string][]byte `json:"platform_sboms"`
}

// DecodeNodeResponses decodes the SBOMs from the node responses. If the
// response does not have SBOMs, nil is returned.
func DecodeNodeResponses(nodeRes depotbuild.DepotNodeResponse) (*SBOM, error) {
	encodedSBOMs, ok := nodeRes.SolveResponse.ExporterResponse[SBOMsLabel]
	if !ok {
		return nil, nil
	}

	r := strings.NewReader(encodedSBOMs)
	b64 := base64.NewDecoder(base64.StdEncoding, r)
	gz, err := gzip.NewReader(b64)
	if err != nil {
		return nil, fmt.Errorf("invalid exported images gzip: %w", err)
	}
	defer gz.Close()

	var sbom SBOM
	err = json.NewDecoder(gz).Decode(&sbom)
	if err != nil {
		return nil, fmt.Errorf("invalid exported images json: %w", err)
	}

	return &sbom, nil
}
