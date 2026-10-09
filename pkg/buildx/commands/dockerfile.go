package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/docker/buildx/build"
	"github.com/docker/buildx/util/urlutil"
)

// dockerfileSource is the Dockerfile of a build target as the client sees it.
type dockerfileSource struct {
	// Filename is the base name that buildkit reports in source locations.
	Filename string
	Content  []byte
	Err      error
}

// readDockerfile reads the Dockerfile of a build target. It returns nil when
// the Dockerfile is not on this machine, for example in a remote context.
func readDockerfile(inp build.Inputs, stdin []byte) *dockerfileSource {
	if inp.DockerfileInline != "" {
		return &dockerfileSource{Filename: "Dockerfile", Content: []byte(inp.DockerfileInline)}
	}
	if urlutil.IsRemoteURL(inp.ContextPath) || urlutil.IsRemoteURL(inp.DockerfilePath) {
		return nil
	}
	if inp.DockerfilePath == "-" {
		return &dockerfileSource{Filename: "Dockerfile", Content: stdin}
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
