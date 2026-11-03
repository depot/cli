package commands

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"slices"

	"github.com/depot/cli/pkg/load"
	"github.com/depot/cli/pkg/sbom"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/sys/atomicwriter"
)

func writeMetadataFile(filename, projectID, buildID string, requestedTargets []string, metadata map[string]any, isBake bool) error {
	depotBuild := struct {
		BuildID   string   `json:"buildID"`
		ProjectID string   `json:"projectID"`
		Targets   []string `json:"targets,omitempty"`
	}{
		BuildID:   buildID,
		ProjectID: projectID,
	}

	if isBake {
		// If requestedTargets was provided, use that; otherwise use all metadata keys
		if len(requestedTargets) > 0 {
			depotBuild.Targets = requestedTargets
		} else {
			depotBuild.Targets = slices.Sorted(maps.Keys(metadata))
		}
	}

	metadata["depot.build"] = depotBuild
	b, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	return atomicwriter.WriteFile(filename, b, 0644)
}

func decodeExporterResponse(exporterResponse map[string]string) map[string]any {
	out := make(map[string]any)
	for k, v := range exporterResponse {
		dt, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			out[k] = v
			continue
		}
		if k == load.ImagesExported {
			_, manifests, imageConfigs, err := load.DecodeExportImages(v)
			if err != nil {
				out[k] = v
			} else {
				out["manifests"] = manifests
				out["imageConfigs"] = imageConfigs
			}

			continue
		}

		// Filter out the SBOMs as they can be quite large.
		if k == sbom.SBOMsLabel {
			continue
		}

		var raw map[string]any
		if err = json.Unmarshal(dt, &raw); err != nil || len(raw) == 0 {
			out[k] = v
			continue
		}
		// DEPOT: Remove the depot specific keys.
		// We use these for fast load and the format is not compatible with the OCI spec.
		if k == exptypes.ExporterImageDescriptorKey {
			if anno, ok := raw["annotations"]; ok {
				if anno, ok := anno.(map[string]any); ok {
					delete(anno, "depot.containerimage.index")
					delete(anno, "depot.containerimage.config")
					delete(anno, "depot.containerimage.manifest")
					out[k] = raw
					continue
				}
			}
		}
		out[k] = json.RawMessage(dt)
	}
	return out
}
