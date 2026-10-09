package docker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"reflect"
	"strings"
	"testing"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/config/configfile"
	configtypes "github.com/docker/cli/cli/config/types"
	"github.com/docker/cli/cli/streams"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/registry"
	dockerclient "github.com/moby/moby/client"
)

const (
	driverImage = "public.ecr.aws/depot/cli:2.101.63"
	mirrorImage = "ghcr.io/depot/cli:2.101.63"
	pullSuccess = "{\"status\":\"Download complete\"}\n"
	pullFailure = "{\"errorDetail\":{\"message\":\"toomanyrequests: Data limit exceeded\"},\"error\":\"toomanyrequests: Data limit exceeded\"}\n"
)

type pullResponse struct {
	body string
	err  error
}

type pullBody struct {
	io.Reader
	closed bool
}

func (b *pullBody) Close() error {
	b.closed = true
	return nil
}

func (b *pullBody) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	panic("unexpected call to JSONMessages")
}

func (b *pullBody) Wait(context.Context) error {
	panic("unexpected call to Wait")
}

type imageClient struct {
	dockerclient.APIClient
	cached    bool
	responses []pullResponse
	pulls     []string
	auth      []string
	bodies    []*pullBody
	tags      [][2]string
	tagErr    error
	cancel    context.CancelFunc
}

func (c *imageClient) ImageList(context.Context, dockerclient.ImageListOptions) (dockerclient.ImageListResult, error) {
	if c.cached {
		return dockerclient.ImageListResult{Items: []image.Summary{{ID: "cached-driver"}}}, nil
	}
	return dockerclient.ImageListResult{}, nil
}

func (c *imageClient) ImagePull(_ context.Context, ref string, opts dockerclient.ImagePullOptions) (dockerclient.ImagePullResponse, error) {
	i := len(c.pulls)
	c.pulls = append(c.pulls, ref)
	c.auth = append(c.auth, opts.RegistryAuth)
	if c.cancel != nil {
		c.cancel()
	}
	if i >= len(c.responses) {
		return nil, errors.New("unexpected image pull")
	}
	response := c.responses[i]
	if response.err != nil {
		return nil, response.err
	}
	body := &pullBody{Reader: strings.NewReader(response.body)}
	c.bodies = append(c.bodies, body)
	return body, nil
}

func (c *imageClient) ImageTag(_ context.Context, opts dockerclient.ImageTagOptions) (dockerclient.ImageTagResult, error) {
	c.tags = append(c.tags, [2]string{opts.Source, opts.Target})
	return dockerclient.ImageTagResult{}, c.tagErr
}

type imageCLI struct {
	command.Cli
	client *imageClient
	config *configfile.ConfigFile
	stderr bytes.Buffer
}

func (c *imageCLI) Client() dockerclient.APIClient     { return c.client }
func (c *imageCLI) ConfigFile() *configfile.ConfigFile { return c.config }
func (c *imageCLI) Err() *streams.Out                  { return streams.NewOut(&c.stderr) }

func TestDownloadImage(t *testing.T) {
	quotaErr := errors.New("toomanyrequests: Data limit exceeded")
	mirrorErr := errors.New("mirror unavailable")
	tagErr := errors.New("tag denied")
	tests := []struct {
		name      string
		image     string
		cached    bool
		responses []pullResponse
		tagErr    error
		cancel    bool
		wantPulls []string
		wantTags  [][2]string
		wantError string
		wantCause error
	}{
		{name: "cached driver needs no registry", cached: true},
		{
			name:      "successful ECR pull needs no mirror",
			responses: []pullResponse{{body: pullSuccess}},
			wantPulls: []string{driverImage},
		},
		{
			name:      "ECR quota failure uses matching GHCR image",
			responses: []pullResponse{{err: quotaErr}, {body: pullSuccess}},
			wantPulls: []string{driverImage, mirrorImage},
			wantTags:  [][2]string{{mirrorImage, driverImage}},
		},
		{
			name:      "quota error in HTTP 200 stream uses mirror",
			responses: []pullResponse{{body: pullFailure}, {body: pullSuccess}},
			wantPulls: []string{driverImage, mirrorImage},
			wantTags:  [][2]string{{mirrorImage, driverImage}},
		},
		{
			name:      "both registries fail",
			responses: []pullResponse{{err: quotaErr}, {err: mirrorErr}},
			wantPulls: []string{driverImage, mirrorImage},
			wantError: "mirror unavailable", wantCause: mirrorErr,
		},
		{
			name:      "mirror stream failure must not tag incomplete image",
			responses: []pullResponse{{err: quotaErr}, {body: pullFailure}},
			wantPulls: []string{driverImage, mirrorImage},
			wantError: "Data limit exceeded",
		},
		{
			name:      "mirror must be tagged for Buildx",
			responses: []pullResponse{{err: quotaErr}, {body: pullSuccess}},
			tagErr:    tagErr,
			wantPulls: []string{driverImage, mirrorImage},
			wantTags:  [][2]string{{mirrorImage, driverImage}},
			wantError: "tag denied", wantCause: tagErr,
		},
		{
			name:      "cancelled pull does not try another registry",
			responses: []pullResponse{{err: context.Canceled}}, cancel: true,
			wantPulls: []string{driverImage},
			wantError: "context canceled", wantCause: context.Canceled,
		},
		{
			name:      "other repositories do not use Depot mirror",
			image:     "public.ecr.aws/other/image:latest",
			responses: []pullResponse{{err: quotaErr}},
			wantPulls: []string{"public.ecr.aws/other/image:latest"},
			wantError: "Data limit exceeded", wantCause: quotaErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &imageClient{cached: tt.cached, responses: tt.responses, tagErr: tt.tagErr}
			if tt.cancel {
				client.cancel = cancel
			}
			cli := &imageCLI{client: client, config: configfile.New("")}
			cli.config.AuthConfigs = map[string]configtypes.AuthConfig{
				"public.ecr.aws": {Username: "AWS", Password: "ecr-credential"},
				"ghcr.io":        {Username: "github-user", Password: "ghcr-credential"},
			}
			image := tt.image
			if image == "" {
				image = driverImage
			}
			err := DownloadImage(ctx, cli, image)
			if tt.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected error containing %q, got %v", tt.wantError, err)
			}
			if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
				t.Fatalf("expected cause %v, got %v", tt.wantCause, err)
			}
			if !reflect.DeepEqual(client.pulls, tt.wantPulls) {
				t.Fatalf("expected pulls %v, got %v", tt.wantPulls, client.pulls)
			}
			if !reflect.DeepEqual(client.tags, tt.wantTags) {
				t.Fatalf("expected tags %v, got %v", tt.wantTags, client.tags)
			}
			for _, body := range client.bodies {
				if !body.closed {
					t.Error("pull response body was not closed")
				}
			}
			for i, ref := range client.pulls {
				data, err := base64.URLEncoding.DecodeString(client.auth[i])
				if err != nil {
					t.Fatal(err)
				}
				var auth registry.AuthConfig
				if err := json.Unmarshal(data, &auth); err != nil {
					t.Fatal(err)
				}
				if ref == driverImage && auth.Password != "ecr-credential" {
					t.Errorf("ECR pull used the wrong credentials: %q", auth.Password)
				}
				if ref == mirrorImage && auth.Password != "ghcr-credential" {
					t.Errorf("GHCR pull used the wrong credentials: %q", auth.Password)
				}
			}
			if len(tt.wantPulls) > 1 && (!strings.Contains(cli.stderr.String(), driverImage) || !strings.Contains(cli.stderr.String(), mirrorImage)) {
				t.Errorf("fallback notice must identify both images: %s", &cli.stderr)
			}
			if tt.name == "both registries fail" && (!strings.Contains(err.Error(), driverImage) || !strings.Contains(err.Error(), mirrorImage) || !strings.Contains(err.Error(), quotaErr.Error())) {
				t.Errorf("error must identify both registries and failures: %v", err)
			}
		})
	}
}
