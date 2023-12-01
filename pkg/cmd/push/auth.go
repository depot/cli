package push

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/containerd/remotes/docker/auth"
	depotapi "github.com/depot/cli/pkg/api"
	"github.com/docker/cli/cli/command"
	configtypes "github.com/docker/cli/cli/config/types"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// GetAuthToken gets an auth token for a registry.
// It does this by loading the local docker auth, determining the authorization schema via a HEAD request,
// and then requesting a token from the realm.
func GetAuthToken(ctx context.Context, dockerCli command.Cli, parsedTag *ParsedTag, manifest ocispecs.Descriptor) (*Token, error) {
	authConfig, err := GetAuthConfig(dockerCli, parsedTag.Host)
	if err != nil {
		return nil, err
	}

	push := true
	scope, err := docker.RepositoryScope(parsedTag.Refspec, push)
	if err != nil {
		return nil, err
	}

	challenge, err := AuthKind(ctx, parsedTag.Refspec, manifest)
	if err != nil {
		return nil, err
	}

	return FetchToken(ctx, authConfig, challenge, []string{scope})
}

// GetAuthConfig gets the auth config from the local docker login.
func GetAuthConfig(dockerCli command.Cli, host string) (*configtypes.AuthConfig, error) {
	if host == "registry-1.docker.io" {
		host = "https://index.docker.io/v1/"
	}

	config, err := dockerCli.ConfigFile().GetAuthConfig(host)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

// AuthKind tries to do a HEAD request to the manifest to try to get the WWW-Authenticate header.
// If HEAD is not supported, it will try to get a GET.  Apparently, this is for older registries.
func AuthKind(ctx context.Context, refspec reference.Spec, manifest ocispecs.Descriptor) (*auth.Challenge, error) {
	// Reversing the refspec's path.Join behavior.
	i := strings.Index(refspec.Locator, "/")
	host, repository := refspec.Locator[:i], refspec.Locator[i+1:]
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}

	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", host, repository, refspec.Object)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Accept", manifest.MediaType)
	req.Header.Add("Accept", `*/*`)
	req.Header.Set("User-Agent", depotapi.Agent())

	// Helper function allowing the HTTP method to change because some registries
	// use GET rather than HEAD (according to an old comment).
	return checkAuthKind(ctx, req)
}

func checkAuthKind(ctx context.Context, req *http.Request) (*auth.Challenge, error) {
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	_ = res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return nil, nil
	case http.StatusUnauthorized:
		challenges := auth.ParseAuthHeader(res.Header)
		if len(challenges) == 0 {
			return nil, fmt.Errorf("no auth challenges found")
		}
		return &challenges[0], nil
	case http.StatusMethodNotAllowed:
		// We have a callback here to allow us to retry the request with a `GET`if the registry doesn't support `HEAD`.
		req.Method = http.MethodGet
		return checkAuthKind(ctx, req)
	}

	return nil, fmt.Errorf("unexpected status code: %s", res.Status)
}
