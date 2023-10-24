package registry

import (
	buildx "github.com/docker/buildx/build"
	"github.com/moby/buildkit/client"
)

type SaveOptions struct {
	OrgID     string
	ProjectID string
	BuildID   string
}

// WithDepotSave adds an output type image with a push to the depot registry.
// If any image exports already exist, they will be updated to push to the depot registry.
func WithDepotSave(buildOpts map[string]buildx.Options, opts SaveOptions) map[string]buildx.Options {
	if opts.OrgID == "" || opts.ProjectID == "" || opts.BuildID == "" {
		return buildOpts
	}

	for target, buildOpt := range buildOpts {
		imageName := DepotImageName(opts.OrgID, opts.ProjectID, opts.BuildID)
		buildOpt.Tags = append(buildOpt.Tags, imageName)

		imageExportIndices := []int{}
		for i, export := range buildOpt.Exports {
			if export.Type == "image" {
				imageExportIndices = append(imageExportIndices, i)
			}
		}

		for _, i := range imageExportIndices {
			buildOpt.Exports[i].Attrs["push"] = "true"
		}

		if len(imageExportIndices) == 0 {
			exportImage := client.ExportEntry{
				Type:  "image",
				Attrs: map[string]string{"push": "true"},
			}
			buildOpt.Exports = append(buildOpt.Exports, exportImage)
		}

		buildOpts[target] = buildOpt
	}

	return buildOpts
}
