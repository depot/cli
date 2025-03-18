package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	depotapi "github.com/depot/cli/pkg/api"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

// PushManifest pushes a manifest to a registry.
func PushManifest(ctx context.Context, registryToken *Token, refspec reference.Spec, tag string, manifest ocispecs.Descriptor, manifestBytes []byte) error {
	// Reversing the refspec's path.Join behavior.
	i := strings.Index(refspec.Locator, "/")
	host, repository := refspec.Locator[:i], refspec.Locator[i+1:]
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}

	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", host, repository, tag)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(manifestBytes))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", depotapi.Agent())
	req.Header.Set("Content-Type", manifest.MediaType)
	req.Header.Set("Authorization", fmt.Sprintf("%s %s", registryToken.Scheme, registryToken.Token))

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 == 2 {
		return nil
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	return fmt.Errorf("unexpected status code: %s %s", res.Status, string(body))
}

type ImageDescriptors struct {
	Indices   []ocispecs.Descriptor `json:"indices,omitempty"`
	Manifests []ocispecs.Descriptor `json:"manifests,omitempty"`
	Configs   []ocispecs.Descriptor `json:"configs,omitempty"`
	Layers    []ocispecs.Descriptor `json:"layers,omitempty"`

	IndexBytes    map[digest.Digest][]byte `json:"indexBytes,omitempty"`
	ManifestBytes map[digest.Digest][]byte `json:"manifestBytes,omitempty"`
}

// GetImageDescriptors returns back all the descriptors for an image.
func GetImageDescriptors(ctx context.Context, token, buildID, target string, logger StartLogDetailFunc) (*ImageDescriptors, error) {
	// Download location and credentials of image save.
	client := depotapi.NewBuildClient()
	req := &cliv1.GetPullInfoRequest{BuildId: buildID}
	res, err := client.GetPullInfo(ctx, depotapi.WithAuthentication(connect.NewRequest(req), token))
	if err != nil {
		return nil, err
	}

	username, password, ref := res.Msg.Username, res.Msg.Password, res.Msg.Reference
	if target != "" {
		ref = ref + "-" + target
	}

	authorizer := &Authorizer{Username: username, Password: password}
	hosts := docker.ConfigureDefaultRegistries(docker.WithAuthorizer(authorizer))

	headers := http.Header{}
	headers.Set("User-Agent", depotapi.Agent())
	resolver := docker.NewResolver(docker.ResolverOptions{
		Hosts:   hosts,
		Headers: headers,
	})

	fin := logger(fmt.Sprintf("Resolving %s", ref))
	name, desc, err := resolver.Resolve(ctx, ref)
	fin()
	if err != nil {
		return nil, err
	}

	mu := sync.Mutex{}
	descs := ImageDescriptors{
		IndexBytes:    map[digest.Digest][]byte{},
		ManifestBytes: map[digest.Digest][]byte{},
	}

	// Recursively fetch all the image descriptors. If a descriptor contains
	// other descriptors, an additional goroutine is spawned on the errgroup.
	errgroup, ctx := errgroup.WithContext(ctx)
	var fetchImageDescriptors func(ctx context.Context, desc ocispecs.Descriptor) error
	fetchImageDescriptors = func(ctx context.Context, desc ocispecs.Descriptor) error {
		fetcher, err := resolver.Fetcher(ctx, name)
		if err != nil {
			return err
		}

		fin := logger(fmt.Sprintf("Fetching manifest %s", desc.Digest.String()))
		buf, err := fetch(ctx, fetcher, desc)
		fin()
		if err != nil {
			return err
		}

		if images.IsIndexType(desc.MediaType) {
			var index ocispecs.Index
			if err := json.Unmarshal(buf, &index); err != nil {
				return err
			}

			mu.Lock()
			descs.Indices = append(descs.Indices, desc)
			descs.IndexBytes[desc.Digest] = buf
			mu.Unlock()

			for _, m := range index.Manifests {
				m := m
				if m.Digest != "" {
					// Only download unique descriptors.
					completed := false
					mu.Lock()
					if _, ok := descs.IndexBytes[m.Digest]; ok {
						completed = true
					}
					if _, ok := descs.ManifestBytes[m.Digest]; ok {
						completed = true
					}
					mu.Unlock()

					if !completed {
						errgroup.Go(func() error {
							return fetchImageDescriptors(ctx, m)
						})
					}
				}
			}
		} else if images.IsManifestType(desc.MediaType) {
			var manifest ocispecs.Manifest
			if err := json.Unmarshal(buf, &manifest); err != nil {
				return err
			}

			mu.Lock()
			descs.Manifests = append(descs.Manifests, desc)
			descs.ManifestBytes[desc.Digest] = buf
			descs.Configs = append(descs.Configs, manifest.Config)
			descs.Layers = append(descs.Layers, manifest.Layers...)
			mu.Unlock()
		}
		return nil
	}

	errgroup.Go(func() error {
		return fetchImageDescriptors(ctx, desc)
	})

	err = errgroup.Wait()
	if err != nil {
		return nil, err
	}

	return &descs, nil
}

func fetch(ctx context.Context, fetcher remotes.Fetcher, desc ocispecs.Descriptor) ([]byte, error) {
	r, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// Authorizer is a static authorizer used to authenticate with the Depot registry.
type Authorizer struct {
	Username string
	Password string
}

func (a *Authorizer) Authorize(ctx context.Context, req *http.Request) error {
	req.SetBasicAuth(a.Username, a.Password)
	return nil
}
func (a *Authorizer) AddResponses(ctx context.Context, responses []*http.Response) error {
	return errdefs.ErrNotImplemented
}
