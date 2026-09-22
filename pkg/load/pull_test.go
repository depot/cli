package load

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
)

type fakePullClient struct {
	docker.APIClient
	body   string
	tagged []string
}

func (f *fakePullClient) ImagePull(ctx context.Context, ref string, opts types.ImagePullOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func (f *fakePullClient) ImageTag(ctx context.Context, source, target string) error {
	f.tagged = append(f.tagged, target)
	return nil
}

const pullErrorStream = `{"status":"Pulling from test/image","id":"latest"}
{"errorDetail":{"message":"unexpected EOF"},"error":"unexpected EOF"}
`

func TestImagePullPrivilegedQuietReturnsStreamError(t *testing.T) {
	client := &fakePullClient{body: pullErrorStream}
	opts := PullOptions{Quiet: true, KeepImage: true, UserTags: []string{"user/image:latest"}}

	err := ImagePullPrivileged(context.Background(), client, "test/image:latest", opts, nil)
	if err == nil {
		t.Fatal("expected error from pull stream")
	}
	var jsonErr *jsonmessage.JSONError
	if !errors.As(err, &jsonErr) || jsonErr.Message != "unexpected EOF" {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.tagged) != 0 {
		t.Fatalf("expected no tags after failed pull, got %v", client.tagged)
	}
}

func TestDecodeDeliversErrorWhenChannelFull(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString(`{"status":"Downloading","id":"abc","progressDetail":{"current":1,"total":2}}` + "\n")
	}
	sb.WriteString(pullErrorStream)

	msgCh := make(chan Message, 1)
	go decode(context.Background(), strings.NewReader(sb.String()), msgCh)

	var got *jsonmessage.JSONError
	for msg := range msgCh {
		if msg.err != nil {
			t.Fatalf("unexpected decode error: %v", msg.err)
		}
		if msg.msg.Error != nil {
			got = msg.msg.Error
		}
	}
	if got == nil || got.Message != "unexpected EOF" {
		t.Fatalf("expected pull error to be delivered, got %v", got)
	}
}
