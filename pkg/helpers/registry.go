package helpers

import (
	"github.com/docker/cli/cli/config/types"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/registry"
)

// Creates a docker registry auth config from a build if the build
// has a registry token and url.  If the build does not have a registry
// AuthConfig will be nil.
func ResolveRegistryAuth(build Build) (*types.AuthConfig, error) {
	if build.RegistryImage == "" || build.RegistryToken == "" {
		return nil, nil
	}

	ref, err := reference.ParseNormalizedNamed(build.RegistryImage)
	if err != nil {
		return nil, err
	}

	repoInfo, err := registry.ParseRepositoryInfo(ref)
	if err != nil {
		return nil, err
	}

	// Add user's private depot registry credentials to the in-memory docker config.
	return &types.AuthConfig{
		ServerAddress: repoInfo.Index.Name,
		Auth:          build.RegistryToken,
	}, nil
}
