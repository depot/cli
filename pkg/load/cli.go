package load

import (
	"fmt"
	"strings"

	"github.com/docker/buildx/build"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
)

// DepotLoadOptions are options to load images from the depot hosted registry.
type DepotLoadOptions struct {
	Project      string      // Depot project name; used to tag images.
	BuildID      string      // Depot build ID; used to tag images.
	IsBake       bool        // If run from bake, we add the bake target to the image tag.
	ProgressMode string      // ProgressMode quiet will not print progress.
	UseRegistry  bool        // If UseRegistry, load build from registry instead of proxy
	BuildCreds   *BuildCreds // If UseRegistry, the credentials for pulling from registry
}

type BuildCreds struct {
	Username string
	Password string
}

// Options to download from the Depot hosted registry and tag the image with the user provide tag.
type PullOptions struct {
	UserTags      []string // Tags the user wishes the image to have.
	Quiet         bool     // No logs plz
	Username      *string  // If set, use this username for the registry.
	Password      *string  // If set, use this password for the registry.
	ServerAddress *string  // If set, use this server address for the registry.
	Platform      *string  // If set, only pull the image if it matches the platform.
	KeepImage     bool     // If set, do not remove the image after pulling and tagging with user tags.
}

// WithDepotImagePull updates buildOpts to push to the depot user's personal registry.
// allowing us to pull layers in parallel from the depot registry.
func WithDepotImagePull(buildOpts map[string]build.Options, loadOpts DepotLoadOptions) (map[string]build.Options, map[string]PullOptions) {
	toPull := make(map[string]PullOptions)
	for target, buildOpt := range buildOpts {
		// Gather all tags the user specifies for this image.
		userTags := buildOpt.Tags

		var shouldPull bool
		// As of today (2023-03-15), buildx only supports one export.
		for _, export := range buildOpt.Exports {
			// Only pull if the user asked for an image export.
			if export.Type == "image" {
				shouldPull = true
				if name, ok := export.Attrs["name"]; ok {
					// "name" is a comma separated list of tags to apply to the image.
					userTags = append(userTags, strings.Split(name, ",")...)
				}
			}
		}

		// If the user did not specify an image export, we add one.
		// This happens when the user specifies `--load` rather than an `--output`
		if len(buildOpt.Exports) == 0 {
			shouldPull = true
			buildOpt.Exports = []client.ExportEntry{{Type: "image"}}
		}

		buildOpts[target] = buildOpt

		if shouldPull {
			// When we pull we need at least one user tag as no tags means that
			// it would otherwise get removed.
			if len(userTags) == 0 {
				userTags = append(userTags, defaultImageName(loadOpts, target))
			}

			pullOpt := PullOptions{
				UserTags: userTags,
				Quiet:    loadOpts.ProgressMode == progress.PrinterModeQuiet,
			}

			if loadOpts.UseRegistry && loadOpts.BuildCreds != nil {
				serverAddress := "registry.depot.dev"
				pullOpt.KeepImage = true
				pullOpt.Username = &loadOpts.BuildCreds.Username
				pullOpt.Password = &loadOpts.BuildCreds.Password
				pullOpt.ServerAddress = &serverAddress
			}

			toPull[target] = pullOpt
		}
	}

	useOCI := true

	// Don't use OCI mediatypes if pushing to Heroku's registry.
	for _, options := range toPull {
		for _, tag := range options.UserTags {
			if strings.Contains(tag, "registry.heroku.com") {
				useOCI = false
				break
			}
		}
	}

	// Add oci-mediatypes for any image build regardless of whether we are pulling.
	// This gives us more flexibility for future options like estargz.
	for target, buildOpt := range buildOpts {
		for i, export := range buildOpt.Exports {
			if export.Type == "image" {
				if export.Attrs == nil {
					export.Attrs = map[string]string{}
				}

				// To export an image via --load the buildkitd logic requires a name.
				if _, ok := export.Attrs["name"]; !ok {
					export.Attrs["name"] = defaultImageName(loadOpts, target)
				}

				if useOCI {
					export.Attrs["oci-mediatypes"] = "true"
				}

				export.Attrs["depot.export.lease"] = "true"
				export.Attrs["depot.export.image.version"] = "2"
			}
			buildOpt.Exports[i] = export
		}
		buildOpts[target] = buildOpt
	}

	return buildOpts, toPull
}

// For backwards compatibility if the API does not support the depot registry,
// we use the previous buildx behavior of pulling the image via the output docker.
// NOTE: this means that a single tar will be sent from buildkit to the client and
// imported into the docker daemon.  This is quite slow.
func WithDockerLoad(buildOpts map[string]build.Options) map[string]build.Options {
	for key, buildOpt := range buildOpts {
		if len(buildOpt.Exports) != 0 {
			continue // assume that exports already has a docker export.
		}
		buildOpt.Exports = []client.ExportEntry{
			{
				Type:  "docker",
				Attrs: map[string]string{},
			},
		}
		buildOpts[key] = buildOpt
	}
	return buildOpts
}

// https://github.com/moby/containerd/blob/96c5ae04b6784e180aaeee50fba715ac448ddb0d/reference/docker/reference.go#L27-L31
func defaultImageName(loadOpts DepotLoadOptions, target string) string {
	invalidNameRunes := func(r rune) rune {
		alpha := 'a' <= r && r <= 'z'
		numeric := '0' <= r && r <= '9'
		sep := r == '-' || r == '_' || r == '.'

		if !alpha && !numeric && !sep {
			return -1
		}
		return r
	}

	invalidTagRunes := func(r rune) rune {
		lower := 'a' <= r && r <= 'z'
		upper := 'A' <= r && r <= 'Z'
		numeric := '0' <= r && r <= '9'
		under := r == '_'

		if !lower && !upper && !numeric && !under {
			return -1
		}
		return r
	}

	project := strings.Map(invalidNameRunes, strings.ToLower(loadOpts.Project))
	project = strings.TrimFunc(project, func(r rune) bool {
		return invalidNameRunes(r) == -1
	})

	buildID := strings.Map(invalidTagRunes, strings.ToLower(loadOpts.BuildID))
	target = strings.Map(invalidTagRunes, strings.ToLower(target))

	defaultImageName := fmt.Sprintf("depot-project-%s:build-%s", project, buildID)
	if loadOpts.IsBake {
		defaultImageName = fmt.Sprintf("%s-%s", defaultImageName, target)
	}

	return defaultImageName
}
