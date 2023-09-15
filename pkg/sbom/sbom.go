package sbom

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"

	depotbuild "github.com/depot/cli/pkg/buildx/build"
)

const SBOMsLabel = "depot/sboms"

type SBOMs map[string][]byte

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
			for platform, sbom := range sboms {
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

func DecodeNodeResponses(nodeRes depotbuild.DepotNodeResponse) (SBOMs, error) {
	sboms := map[string][]byte{}
	encodedSBOMs, ok := nodeRes.SolveResponse.ExporterResponse[SBOMsLabel]
	if !ok {
		return sboms, nil
	}

	jsonSBOMs, err := base64.StdEncoding.DecodeString(encodedSBOMs)
	if err != nil {
		return nil, fmt.Errorf("invalid exported images encoding: %w", err)
	}

	if err := json.Unmarshal(jsonSBOMs, &sboms); err != nil {
		return nil, fmt.Errorf("invalid exported images json: %w", err)
	}

	return sboms, nil
}
