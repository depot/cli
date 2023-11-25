package push

import (
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes/docker"
	ref "github.com/distribution/reference"
)

type ParsedTag struct {
	Host    string
	Path    string
	Refspec reference.Spec
	Tag     string
}

// ParseTag parses a tag into its components used as a destination for pushing.
func ParseTag(tag string) (*ParsedTag, error) {
	named, err := ref.ParseNormalizedNamed(tag)
	if err != nil {
		return nil, err
	}

	domain := ref.Domain(named)
	path := ref.Path(named)
	named = ref.TagNameOnly(named)
	var imageTag string
	if r, ok := named.(ref.Tagged); ok {
		imageTag = r.Tag()
	}

	host, _ := docker.DefaultHost(domain)

	refspec, err := reference.Parse(named.String())
	if err != nil {
		return nil, err
	}

	return &ParsedTag{Host: host, Path: path, Refspec: refspec, Tag: imageTag}, nil
}
