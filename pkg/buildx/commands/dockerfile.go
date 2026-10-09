package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/docker/buildx/build"
	"github.com/docker/buildx/util/urlutil"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
)

// dockerfileSource is the Dockerfile of a build target as the client sees it.
type dockerfileSource struct {
	// Filename is the base name that buildkit reports in source locations.
	Filename string
	Content  []byte
	Err      error
	// URL is the address of a Dockerfile that the builder must download.
	URL string
}

// readDockerfile reads the Dockerfile of a build target. It returns nil when
// the Dockerfile is in a remote context.
func readDockerfile(inp build.Inputs, stdin []byte) *dockerfileSource {
	switch {
	case urlutil.IsHTTPURL(inp.DockerfilePath):
		return &dockerfileSource{Filename: "Dockerfile", URL: inp.DockerfilePath}
	case inp.DockerfileInline != "":
		return &dockerfileSource{Filename: "Dockerfile", Content: []byte(inp.DockerfileInline)}
	case inp.DockerfilePath == "-":
		return &dockerfileSource{Filename: "Dockerfile", Content: stdin}
	case urlutil.IsRemoteURL(inp.ContextPath):
		return nil
	}
	if inp.ContextPath == "-" {
		if !isArchive(stdin) {
			return &dockerfileSource{Filename: "Dockerfile", Content: stdin}
		}
		name := inp.DockerfilePath
		if name == "" {
			name = "Dockerfile"
		}
		content, err := readArchiveFile(stdin, name)
		return &dockerfileSource{Filename: filepath.Base(name), Content: content, Err: err}
	}

	path := inp.DockerfilePath
	if path == "" {
		path = lowercaseDockerfile(filepath.Join(inp.ContextPath, "Dockerfile"))
	}
	content, err := os.ReadFile(path)
	return &dockerfileSource{Filename: filepath.Base(path), Content: content, Err: err}
}

// downloadDockerfile downloads a Dockerfile on the builder, in the same way
// as buildx downloads it for the build.
func downloadDockerfile(ctx context.Context, c *client.Client, url string) ([]byte, error) {
	var content []byte
	_, err := c.Build(ctx, client.SolveOpt{Internal: true}, "buildx", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		def, err := llb.HTTP(url, llb.Filename("Dockerfile")).Marshal(ctx)
		if err != nil {
			return nil, err
		}
		res, err := c.Solve(ctx, gateway.SolveRequest{Definition: def.ToPB()})
		if err != nil {
			return nil, err
		}
		ref, err := res.SingleRef()
		if err != nil {
			return nil, err
		}
		content, err = ref.ReadFile(ctx, gateway.ReadRequest{Filename: "Dockerfile"})
		return nil, err
	}, nil)
	return content, err
}

// lowercaseDockerfile returns the path of "dockerfile" when "Dockerfile" does
// not exist, in the same way as the buildx client.
func lowercaseDockerfile(path string) string {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		lowercase := filepath.Join(filepath.Dir(path), "dockerfile")
		if _, err := os.Lstat(lowercase); err == nil {
			return lowercase
		}
	}
	return path
}

func isArchive(data []byte) bool {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		return true
	}
	return len(data) >= 262 && bytes.Equal(data[257:262], []byte("ustar"))
}

func readArchiveFile(data []byte, name string) ([]byte, error) {
	var r io.Reader = bytes.NewReader(data)
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	want := filepath.Clean(name)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, os.ErrNotExist
		}
		if err != nil {
			return nil, err
		}
		if filepath.Clean(hdr.Name) == want {
			return io.ReadAll(tr)
		}
	}
}
