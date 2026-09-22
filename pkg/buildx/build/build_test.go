package build

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/depot/cli/pkg/buildx/imagetools"
	"github.com/distribution/reference"
	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestCombineBuildManifestsWithAnnotations(t *testing.T) {
	const source = "https://example.com/repo"
	manifestAnnotations := map[string]string{
		"org.opencontainers.image.source": source,
		"com.example.build-id":            "build-123",
	}

	// Serve the per-platform manifests that BuildKit has already annotated.
	manifests := make(map[string][]byte)
	var descs []ocispec.Descriptor
	for _, arch := range []string{"amd64", "arm64"} {
		dt, err := json.Marshal(ocispec.Manifest{
			Versioned:   specs.Versioned{SchemaVersion: 2},
			MediaType:   ocispec.MediaTypeImageManifest,
			Config:      ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromString(arch), Size: 1},
			Layers:      []ocispec.Descriptor{},
			Annotations: manifestAnnotations,
		})
		require.NoError(t, err)
		desc := ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    digest.FromBytes(dt),
			Size:      int64(len(dt)),
			Platform:  &ocispec.Platform{OS: "linux", Architecture: arch},
		}
		manifests["/v2/test/manifests/"+desc.Digest.String()] = dt
		descs = append(descs, desc)
	}
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dt, ok := manifests[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
		_, _ = w.Write(dt)
	}))
	t.Cleanup(registry.Close)
	ref, err := reference.ParseNormalizedNamed(strings.TrimPrefix(registry.URL, "http://") + "/test:latest")
	require.NoError(t, err)

	for _, tc := range []struct {
		name             string
		typedAnnotations bool
	}{
		{name: "bare annotations"},
		{name: "bare and typed annotations", typedAnnotations: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := map[string]string{
				"push": "true",
				"annotation.org.opencontainers.image.source": source,
				"annotation.com.example.build-id":            "build-123",
			}
			if tc.typedAnnotations {
				attrs["annotation-index.org.opencontainers.image.source"] = "https://example.com/index"
				attrs["annotation-manifest-descriptor.com.example.role"] = "platform"
				attrs["annotation-manifest.com.example.build-id"] = "build-123"
			}
			originalAttrs := maps.Clone(attrs)
			annotations, err := extractIndexAnnotations([]client.ExportEntry{{Type: "image", Attrs: attrs}})
			require.NoError(t, err)
			require.Equal(t, originalAttrs, attrs, "preserve annotations for the BuildKit exporter")

			srcs := make([]*imagetools.Source, len(descs))
			for i, desc := range descs {
				srcs[i] = &imagetools.Source{Desc: desc, Ref: ref}
			}
			dt, _, err := imagetools.New(imagetools.Opt{}).Combine(context.Background(), srcs, annotations)
			require.NoError(t, err)

			var index ocispec.Index
			require.NoError(t, json.Unmarshal(dt, &index))
			require.Equal(t, ocispec.MediaTypeImageIndex, index.MediaType)
			require.Len(t, index.Manifests, len(descs))
			if tc.typedAnnotations {
				require.Equal(t, map[string]string{"org.opencontainers.image.source": "https://example.com/index"}, index.Annotations)
			} else {
				require.Empty(t, index.Annotations, "bare annotations belong to manifests, not the index")
			}
			for i, desc := range index.Manifests {
				require.Equal(t, descs[i].Digest, desc.Digest, "retain the annotated platform manifest")
				require.Equal(t, descs[i].Platform, desc.Platform)
				if tc.typedAnnotations {
					require.Equal(t, map[string]string{"com.example.role": "platform"}, desc.Annotations)
				} else {
					require.Empty(t, desc.Annotations)
				}
			}
		})
	}
}
