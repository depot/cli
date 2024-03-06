package imagetools

import (
	"regexp"
	"strings"

	"github.com/containerd/containerd/platforms"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

func ParseAnnotations(inp []string) (map[exptypes.AnnotationKey]string, error) {
	// TODO: use buildkit's annotation parser once it supports setting custom prefix and ":" separator
	annotationRegexp := regexp.MustCompile(`^(?:([a-z-]+)(?:\[([A-Za-z0-9_/-]+)\])?:)?(\S+)$`)
	annotations := make(map[exptypes.AnnotationKey]string)
	for _, inp := range inp {
		k, v, ok := strings.Cut(inp, "=")
		if !ok {
			return nil, errors.Errorf("invalid annotation %q, expected key=value", inp)
		}

		groups := annotationRegexp.FindStringSubmatch(k)
		if groups == nil {
			return nil, errors.Errorf("invalid annotation format, expected <type>:<key>=<value>, got %q", inp)
		}

		typ, platform, key := groups[1], groups[2], groups[3]
		switch typ {
		case "":
		case exptypes.AnnotationIndex, exptypes.AnnotationIndexDescriptor, exptypes.AnnotationManifest, exptypes.AnnotationManifestDescriptor:
		default:
			return nil, errors.Errorf("unknown annotation type %q", typ)
		}

		var ociPlatform *ocispecs.Platform
		if platform != "" {
			p, err := platforms.Parse(platform)
			if err != nil {
				return nil, errors.Wrapf(err, "invalid platform %q", platform)
			}
			ociPlatform = &p
		}

		ak := exptypes.AnnotationKey{
			Type:     typ,
			Platform: ociPlatform,
			Key:      key,
		}
		annotations[ak] = v
	}
	return annotations, nil
}
