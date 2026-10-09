package commands

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/docker/buildx/build"
	"github.com/docker/buildx/util/urlutil"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/dfgitutil"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/util/archiveutil"
	"github.com/moby/go-archive/compression"
)

// dockerfileSource is the Dockerfile of a build target as the client sees it.
type dockerfileSource struct {
	// Filename is the base name that buildkit reports in source locations.
	Filename string
	Content  []byte
	Err      error
	// Fetch reads a Dockerfile that only the builder can download. It is nil
	// when Content holds the Dockerfile.
	Fetch func(ctx context.Context, c *client.Client) ([]byte, error)
}

// readDockerfile reads the Dockerfile of a build target.
func readDockerfile(inp build.Inputs, stdin []byte) *dockerfileSource {
	switch {
	case urlutil.IsHTTPURL(inp.DockerfilePath):
		return &dockerfileSource{Filename: "Dockerfile", Fetch: func(ctx context.Context, c *client.Client) ([]byte, error) {
			return readBuilderFile(ctx, c, llb.HTTP(inp.DockerfilePath, llb.Filename("Dockerfile")), "Dockerfile")
		}}
	case inp.DockerfileInline != "":
		return &dockerfileSource{Filename: "Dockerfile", Content: []byte(inp.DockerfileInline)}
	case inp.DockerfilePath == "-":
		return &dockerfileSource{Filename: "Dockerfile", Content: stdin}
	case urlutil.IsRemoteURL(inp.ContextPath):
		name := inp.DockerfilePath
		if name == "" {
			name = "Dockerfile"
		}
		return &dockerfileSource{Filename: path.Base(name), Fetch: func(ctx context.Context, c *client.Client) ([]byte, error) {
			return readRemoteContextFile(ctx, c, inp.ContextPath, name)
		}}
	}
	if inp.ContextPath == "-" {
		if !archiveutil.IsArchive(stdin) {
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

// readRemoteContextFile reads a file from a git repository or HTTP context
// on the builder. An HTTP context is either an archive or a Dockerfile.
func readRemoteContextFile(ctx context.Context, c *client.Client, contextURL, name string) ([]byte, error) {
	if ref, isGit, err := dfgitutil.ParseGitRef(contextURL); isGit {
		if err != nil {
			return nil, err
		}
		commit := ref.Ref
		if commit == "" {
			commit = ref.Checksum
		}
		return readBuilderFile(ctx, c, llb.Git(ref.Remote, commit), path.Join(ref.SubDir, name))
	}
	data, err := readBuilderFile(ctx, c, llb.HTTP(contextURL, llb.Filename("context")), "context")
	if err != nil || !archiveutil.IsArchive(data) {
		return data, err
	}
	return readArchiveFile(data, name)
}

// readBuilderFile reads a file from a state that the builder solves.
func readBuilderFile(ctx context.Context, c *client.Client, st llb.State, name string) ([]byte, error) {
	var content []byte
	_, err := c.Build(ctx, client.SolveOpt{Internal: true}, "buildx", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		def, err := st.Marshal(ctx)
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
		content, err = ref.ReadFile(ctx, gateway.ReadRequest{Filename: name})
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

// readArchiveFile reads a file from a tar archive in the same way as the
// archive is extracted: a later entry replaces an earlier entry with the same
// path, and links are followed.
func readArchiveFile(data []byte, name string) ([]byte, error) {
	want := archivePath(name)
	for range maxArchiveLinks {
		hdr, content, err := lastArchiveEntry(data, want)
		if err != nil {
			return nil, err
		}
		switch hdr.Typeflag {
		case tar.TypeSymlink:
			if path.IsAbs(hdr.Linkname) {
				want = archivePath(hdr.Linkname)
			} else {
				want = archivePath(path.Join(path.Dir(want), hdr.Linkname))
			}
		case tar.TypeLink:
			want = archivePath(hdr.Linkname)
		default:
			return content, nil
		}
	}
	return nil, fmt.Errorf("%s: too many links", name)
}

const maxArchiveLinks = 40

func archivePath(name string) string {
	return strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(name)), "/")
}

func lastArchiveEntry(data []byte, name string) (*tar.Header, []byte, error) {
	r, err := compression.DecompressStream(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()

	var (
		found   *tar.Header
		content []byte
	)
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if archivePath(hdr.Name) == name {
			found = hdr
			if content, err = io.ReadAll(tr); err != nil {
				return nil, nil, err
			}
		}
	}
	if found == nil {
		return nil, nil, os.ErrNotExist
	}
	return found, content, nil
}
