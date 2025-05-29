package registry

import (
	"github.com/depot/cli/pkg/build"
	buildx "github.com/docker/buildx/build"
	"github.com/moby/buildkit/client"
	"golang.org/x/exp/slices"
)

type SaveOptions struct {
	AdditionalTags        []string
	AdditionalCredentials []build.Credential
	ProjectID             string
	BuildID               string
	// AddTargetSuffix adds the target suffix to the additional tags.
	// Useful for bake targets.
	AddTargetSuffix bool
}

// WithDepotSave adds an output type image with a push to the depot registry.
// If any image exports already exist, they will be updated to push to the depot registry.
func WithDepotSave(buildOpts map[string]buildx.Options, opts SaveOptions) map[string]buildx.Options {
	if opts.ProjectID == "" || opts.BuildID == "" || len(opts.AdditionalTags) == 0 {
		return buildOpts
	}

	for target, buildOpt := range buildOpts {
		buildOpt.Session = ReplaceDockerAuth(opts.AdditionalCredentials, buildOpt.Session)

		hadPush := false
		imageExportIndices := []int{}
		for i, export := range buildOpt.Exports {
			if export.Type == "image" {
				imageExportIndices = append(imageExportIndices, i)
			}
		}

		exports := deepCloneExports(buildOpt.Exports)

		for _, i := range imageExportIndices {
			_, ok := exports[i].Attrs["push"]
			hadPush = hadPush || ok
			exports[i].Attrs["push"] = "true"
		}

		if len(imageExportIndices) == 0 {
			exportImage := client.ExportEntry{
				Type:  "image",
				Attrs: map[string]string{"push": "true"},
			}
			exports = append(exports, exportImage)
		}

		buildOpt.Exports = exports

		additionalTags := slices.Clone(opts.AdditionalTags)
		for i, tag := range additionalTags {
			if opts.AddTargetSuffix {
				additionalTags[i] = tag + "-" + target
			}
		}

		// If the user did not specify push then we do not want to push any
		// tags that were specified.  We strip those tags to avoid pushing.
		if !hadPush {
			buildOpt.Tags = additionalTags
		} else {
			buildOpt.Tags = append(buildOpt.Tags, additionalTags...)
		}
		buildOpts[target] = buildOpt
	}

	return buildOpts
}

func deepCloneExports(original []client.ExportEntry) []client.ExportEntry {
	if original == nil {
		return nil
	}

	exports := make([]client.ExportEntry, len(original))
	for i := range original {
		clone := original[i]
		// Attrs is the only field with references
		clone.Attrs = make(map[string]string, len(original[i].Attrs))
		for k, v := range original[i].Attrs {
			clone.Attrs[k] = v
		}

		exports[i] = clone
	}

	return exports
}
