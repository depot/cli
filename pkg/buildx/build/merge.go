package build

// DEPOT: Copied from imagetools/create.go to filter depot annotations out
// of merged (aka Combined) multi-platform images.
import (
	"context"
	"encoding/json"
	"strings"

	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/platforms"
	"github.com/docker/buildx/util/imagetools"
	"github.com/opencontainers/go-digest"
	imagespecs "github.com/opencontainers/image-spec/specs-go"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

func Combine(ctx context.Context, r *imagetools.Resolver, srcs []*imagetools.Source) ([]byte, specs.Descriptor, error) {
	eg, ctx := errgroup.WithContext(ctx)

	dts := make([][]byte, len(srcs))
	for i := range dts {
		func(i int) {
			eg.Go(func() error {
				dt, err := r.GetDescriptor(ctx, srcs[i].Ref.String(), srcs[i].Desc)
				if err != nil {
					return err
				}
				dts[i] = dt

				if srcs[i].Desc.MediaType == "" {
					mt, err := detectMediaType(dt)
					if err != nil {
						return err
					}
					srcs[i].Desc.MediaType = mt
				}

				mt := srcs[i].Desc.MediaType

				switch mt {
				case images.MediaTypeDockerSchema2Manifest, specs.MediaTypeImageManifest:
					p := srcs[i].Desc.Platform
					if srcs[i].Desc.Platform == nil {
						p = &specs.Platform{}
					}
					if p.OS == "" || p.Architecture == "" {
						if err := loadPlatform(ctx, r, p, srcs[i].Ref.String(), dt); err != nil {
							return err
						}
					}
					srcs[i].Desc.Platform = p
				case images.MediaTypeDockerSchema1Manifest:
					return errors.Errorf("schema1 manifests are not allowed in manifest lists")
				}

				return nil
			})
		}(i)
	}

	if err := eg.Wait(); err != nil {
		return nil, specs.Descriptor{}, err
	}

	// on single source, return original bytes
	if len(srcs) == 1 {
		if mt := srcs[0].Desc.MediaType; mt == images.MediaTypeDockerSchema2ManifestList || mt == specs.MediaTypeImageIndex {
			return dts[0], srcs[0].Desc, nil
		}
	}

	m := map[digest.Digest]int{}
	newDescs := make([]specs.Descriptor, 0, len(srcs))

	addDesc := func(d specs.Descriptor) {
		idx, ok := m[d.Digest]
		if ok {
			old := newDescs[idx]
			if old.MediaType == "" {
				old.MediaType = d.MediaType
			}
			if d.Platform != nil {
				old.Platform = d.Platform
			}
			if old.Annotations == nil {
				old.Annotations = map[string]string{}
			}
			for k, v := range d.Annotations {
				// DEPOT: filter out depot annotations as GCR refuses to accept them.
				if strings.HasPrefix(k, "depot") {
					continue
				}
				old.Annotations[k] = v
			}
			newDescs[idx] = old
		} else {
			m[d.Digest] = len(newDescs)
			newDesc := specs.Descriptor{
				MediaType:    d.MediaType,
				Digest:       d.Digest,
				Size:         d.Size,
				URLs:         d.URLs,
				Annotations:  map[string]string{},
				Data:         d.Data,
				Platform:     d.Platform,
				ArtifactType: d.ArtifactType,
			}
			for k, v := range d.Annotations {
				// DEPOT: filter out depot annotations as GCR refuses to accept them.
				if strings.HasPrefix(k, "depot") {
					continue
				}
				newDesc.Annotations[k] = v
			}

			newDescs = append(newDescs, newDesc)
		}
	}

	for i, src := range srcs {
		switch src.Desc.MediaType {
		case images.MediaTypeDockerSchema2ManifestList, specs.MediaTypeImageIndex:
			var mfst specs.Index
			if err := json.Unmarshal(dts[i], &mfst); err != nil {
				return nil, specs.Descriptor{}, errors.WithStack(err)
			}
			for _, d := range mfst.Manifests {
				addDesc(d)
			}
		default:
			addDesc(src.Desc)
		}
	}

	mt := images.MediaTypeDockerSchema2ManifestList //ocispec.MediaTypeImageIndex
	idx := struct {
		// MediaType is reserved in the OCI spec but
		// excluded from go types.
		MediaType string `json:"mediaType,omitempty"`

		specs.Index
	}{
		MediaType: mt,
		Index: specs.Index{
			Versioned: imagespecs.Versioned{
				SchemaVersion: 2,
			},
			Manifests: newDescs,
		},
	}

	idxBytes, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, specs.Descriptor{}, errors.Wrap(err, "failed to marshal index")
	}

	return idxBytes, specs.Descriptor{
		MediaType: mt,
		Size:      int64(len(idxBytes)),
		Digest:    digest.FromBytes(idxBytes),
	}, nil
}

func detectMediaType(dt []byte) (string, error) {
	var mfst struct {
		MediaType string          `json:"mediaType"`
		Config    json.RawMessage `json:"config"`
		FSLayers  []string        `json:"fsLayers"`
	}

	if err := json.Unmarshal(dt, &mfst); err != nil {
		return "", errors.WithStack(err)
	}

	if mfst.MediaType != "" {
		return mfst.MediaType, nil
	}
	if mfst.Config != nil {
		return images.MediaTypeDockerSchema2Manifest, nil
	}
	if len(mfst.FSLayers) > 0 {
		return images.MediaTypeDockerSchema1Manifest, nil
	}

	return images.MediaTypeDockerSchema2ManifestList, nil
}

func loadPlatform(ctx context.Context, r *imagetools.Resolver, p2 *specs.Platform, in string, dt []byte) error {
	var manifest specs.Manifest
	if err := json.Unmarshal(dt, &manifest); err != nil {
		return errors.WithStack(err)
	}

	dt, err := r.GetDescriptor(ctx, in, manifest.Config)
	if err != nil {
		return err
	}

	var p specs.Platform
	if err := json.Unmarshal(dt, &p); err != nil {
		return errors.WithStack(err)
	}

	p = platforms.Normalize(p)

	if p2.Architecture == "" {
		p2.Architecture = p.Architecture
		if p2.Variant == "" {
			p2.Variant = p.Variant
		}
	}
	if p2.OS == "" {
		p2.OS = p.OS
	}

	return nil
}
