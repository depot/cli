package load

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
)

type pullDockerClient struct {
	docker.APIClient
	body    io.ReadCloser
	pullErr error
	tagErr  error
	tags    []string
	removed bool
}

func (c *pullDockerClient) ImagePull(context.Context, string, types.ImagePullOptions) (io.ReadCloser, error) {
	return c.body, c.pullErr
}

func (c *pullDockerClient) ImageTag(_ context.Context, _, tag string) error {
	c.tags = append(c.tags, tag)
	return c.tagErr
}

func (c *pullDockerClient) ImageRemove(context.Context, string, types.ImageRemoveOptions) ([]types.ImageDeleteResponseItem, error) {
	c.removed = true
	return nil, nil
}

type pullResponseBody struct {
	io.Reader
	closed bool
}

func (b *pullResponseBody) Close() error {
	b.closed = true
	return nil
}

func TestQuietPullFailure(t *testing.T) {
	transportErr := errors.New("connection reset during pull")
	progress := "{\"status\":\"Downloading\",\"id\":\"layer\",\"progressDetail\":{\"current\":1,\"total\":10}}\n"
	tests := []struct {
		name   string
		reader func() io.Reader
		want   string
		cause  error
		code   int
	}{
		{name: "Docker error details", reader: func() io.Reader {
			return strings.NewReader(progress + `{"errorDetail":{"code":503,"message":"registry temporarily unavailable"},"error":"less specific error"}`)
		}, want: "registry temporarily unavailable", code: 503},
		{name: "legacy Docker error", reader: func() io.Reader { return strings.NewReader(progress + `{"error":"pull access denied"}`) }, want: "pull access denied"},
		{name: "malformed JSON", reader: func() io.Reader { return strings.NewReader(progress + `not JSON`) }, want: "invalid character"},
		{name: "truncated JSON", reader: func() io.Reader { return strings.NewReader(progress + `{"status":`) }, want: "unexpected EOF", cause: io.ErrUnexpectedEOF},
		{name: "transport error", reader: func() io.Reader { return io.MultiReader(strings.NewReader(progress), iotest.ErrReader(transportErr)) }, want: transportErr.Error(), cause: transportErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, tags := range [][]string{nil, {"local:latest"}} {
				body := &pullResponseBody{Reader: tt.reader()}
				client := &pullDockerClient{body: body, tagErr: errors.New("No such image")}
				err := ImagePullPrivileged(context.Background(), client, "registry.depot.dev/project:tag", PullOptions{Quiet: true, UserTags: tags}, nil)
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("tags %v: got error %v, want %q", tags, err, tt.want)
				}
				if tt.cause != nil && !errors.Is(err, tt.cause) {
					t.Errorf("got error %v, want cause %v", err, tt.cause)
				}
				if tt.code != 0 {
					var detail *jsonmessage.JSONError
					if !errors.As(err, &detail) || detail.Code != tt.code {
						t.Errorf("Docker error details were lost: %v", err)
					}
				}
				if len(client.tags) != 0 || client.removed {
					t.Errorf("failed pull modified image: tags=%v, removed=%v", client.tags, client.removed)
				}
				if !body.closed {
					t.Error("pull response was not closed")
				}
			}
		})
	}
}

func TestQuietPullSuccess(t *testing.T) {
	for _, keepImage := range []bool{false, true} {
		body := &pullResponseBody{Reader: strings.NewReader(`{"status":"Downloading","id":"layer","progressDetail":{"current":1,"total":10}}
{"status":"Pull complete","id":"layer"}
{"status":"Downloaded newer image"}
`)}
		tags := []string{"local:latest", "local:other"}
		client := &pullDockerClient{body: body}
		// A nil logger ensures quiet mode never tries to emit progress or log messages.
		err := ImagePullPrivileged(context.Background(), client, "registry.depot.dev/project:tag", PullOptions{Quiet: true, UserTags: tags, KeepImage: keepImage}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(client.tags, tags) {
			t.Errorf("got tags %v, want %v", client.tags, tags)
		}
		if client.removed == keepImage {
			t.Errorf("KeepImage=%v, removed=%v", keepImage, client.removed)
		}
		if !body.closed {
			t.Error("pull response was not closed")
		}
	}
}

func TestQuietPullRequestFailure(t *testing.T) {
	want := errors.New("Docker daemon unavailable")
	client := &pullDockerClient{pullErr: want}
	err := ImagePullPrivileged(context.Background(), client, "registry.depot.dev/project:tag", PullOptions{Quiet: true, UserTags: []string{"local:latest"}}, nil)
	if !errors.Is(err, want) {
		t.Errorf("got %v, want %v", err, want)
	}
	if len(client.tags) != 0 || client.removed {
		t.Error("failed pull modified image")
	}
}
