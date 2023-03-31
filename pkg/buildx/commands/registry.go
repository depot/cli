package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/proxy"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// Registry is a small docker registry that serves a single image by loading
// manifests and blobs from a buildkitd cache.
type Registry struct {
	Client           contentapi.ContentClient
	ImageConfig      ocispecs.Descriptor
	ImageManifest    map[digest.Digest]ocispecs.Manifest
	RawImageManifest map[digest.Digest][]byte
}

func NewRegistry(client contentapi.ContentClient, imageConfig ocispecs.Descriptor) *Registry {
	return &Registry{
		Client:           client,
		ImageConfig:      imageConfig,
		ImageManifest:    map[digest.Digest]ocispecs.Manifest{},
		RawImageManifest: map[digest.Digest][]byte{},
	}
}

type Config struct {
	Size        int
	Digest      digest.Digest
	ContentType string
}

func (r *Registry) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	if isBlob(req) {
		r.handleBlobs(resp, req)
		return
	}

	if isManifest(req) {
		r.handleManifests(resp, req)
		return
	}

	resp.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	if req.URL.Path != "/v2/" && req.URL.Path != "/v2" {
		writeError(resp, http.StatusNotFound, "METHOD_UNKNOWN", "We don't understand your method + url")
		return
	}
	resp.WriteHeader(200)
}

// Returns whether this url should be handled by the blob handler
// This is complicated because blob is indicated by the trailing path, not the leading path.
// https://github.com/opencontainers/distribution-spec/blob/master/spec.md#pulling-a-layer
// https://github.com/opencontainers/distribution-spec/blob/master/spec.md#pushing-a-layer
func isBlob(req *http.Request) bool {
	elem := strings.Split(req.URL.Path, "/")
	elem = elem[1:]
	if elem[len(elem)-1] == "" {
		elem = elem[:len(elem)-1]
	}
	if len(elem) < 3 {
		return false
	}
	return elem[len(elem)-2] == "blobs" || (elem[len(elem)-3] == "blobs" &&
		elem[len(elem)-2] == "uploads")
}

func isManifest(req *http.Request) bool {
	elems := strings.Split(req.URL.Path, "/")
	elems = elems[1:]
	if len(elems) < 4 {
		return false
	}
	return elems[len(elems)-2] == "manifests"
}

func (r *Registry) handleBlobs(resp http.ResponseWriter, req *http.Request) {
	elem := strings.Split(req.URL.Path, "/")
	elem = elem[1:]
	if elem[len(elem)-1] == "" {
		elem = elem[:len(elem)-1]
	}
	// Must have a path of form /v2/{name}/blobs/{upload,sha256:}
	if len(elem) < 4 {
		writeError(resp, http.StatusBadRequest, "NAME_INVALID", "blobs must be attached to a repo")
		return
	}
	target := elem[len(elem)-1]
	theSHA := target

	switch req.Method {
	case http.MethodHead:
		manifest, ok := r.ImageManifest[r.ImageConfig.Digest]
		if !ok {
			writeError(resp, http.StatusNotFound, "BLOB_UNKNOWN", "Unknown blob")
			return
		}

		for _, layer := range manifest.Layers {
			if layer.Digest.String() == theSHA {
				resp.Header().Set("Content-Length", strconv.FormatInt(int64(layer.Size), 10))
				resp.Header().Set("Docker-Content-Digest", theSHA)
				resp.WriteHeader(http.StatusOK)
				return
			}
		}

		writeError(resp, http.StatusNotFound, "BLOB_UNKNOWN", "Unknown blob")
		return
	case http.MethodGet:
		manifest, ok := r.ImageManifest[r.ImageConfig.Digest]
		if !ok {
			writeError(resp, http.StatusNotFound, "BLOB_UNKNOWN", "Unknown blob")
			return
		}

		for _, layer := range manifest.Layers {
			if layer.Digest.String() == theSHA {
				resp.Header().Set("Content-Length", strconv.FormatInt(int64(layer.Size), 10))
				resp.Header().Set("Docker-Content-Digest", theSHA)
				resp.WriteHeader(http.StatusOK)
				break
			}
		}
		rr := &contentapi.ReadContentRequest{
			Digest: digest.Digest(theSHA),
		}
		// TODO: context
		childCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rc, err := r.Client.Read(childCtx, rr)
		if err != nil {
			writeError(resp, http.StatusNotFound, "BLOB_UNKNOWN", "Unknown blob")
			return
		}

		for {
			res, err := rc.Recv()
			if err == io.EOF {
				break
			}

			if err != nil {
				// TODO: this should be a log instead.  headers have been written
				writeError(resp, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "read error")
				return
			}
			_, err = resp.Write(res.Data)
			if err != nil {
				// TODO: this should be a log instead.  headers have been written
				writeError(resp, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "client error")
				return
			}
		}

		return

	default:
		writeError(resp, http.StatusBadRequest, "METHOD_UNKNOWN", "We don't understand your method + url")
	}
}

// https://github.com/opencontainers/distribution-spec/blob/master/spec.md#pulling-an-image-manifest
// https://github.com/opencontainers/distribution-spec/blob/master/spec.md#pushing-an-image
func (r *Registry) handleManifests(resp http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		manifestDigest := r.ImageConfig.Digest
		manifest, ok := r.RawImageManifest[manifestDigest]
		if !ok {
			store := proxy.NewContentStore(r.Client)
			// TODO: context
			ra, err := store.ReaderAt(context.TODO(), ocispecs.Descriptor{
				Digest: digest.Digest(manifestDigest),
			})
			if err != nil {
				writeError(resp, http.StatusNotFound, "BLOB_UNKNOWN", "Unknown blob")
				return
			}
			defer ra.Close()

			octets := bytes.Buffer{}
			_, err = io.Copy(&octets, content.NewReader(ra))
			if err != nil {
				writeError(resp, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "Cannot download manifest")
				return
			}

			parsedManifest := ocispecs.Manifest{}
			if err := json.Unmarshal(octets.Bytes(), &parsedManifest); err != nil {
				writeError(resp, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "Invalid manifest json")
				return
			}

			manifest = octets.Bytes()
			r.RawImageManifest[manifestDigest] = octets.Bytes()
			r.ImageManifest[manifestDigest] = parsedManifest
		}

		resp.Header().Set("Docker-Content-Digest", r.ImageConfig.Digest.String())
		resp.Header().Set("Content-Type", r.ImageConfig.MediaType)
		resp.Header().Set("Content-Length", strconv.FormatInt(int64(r.ImageConfig.Size), 10))
		resp.WriteHeader(http.StatusOK)

		_, _ = io.Copy(resp, bytes.NewReader(manifest))
		return

	case http.MethodHead:
		resp.Header().Set("Docker-Content-Digest", r.ImageConfig.Digest.String())
		resp.Header().Set("Content-Type", r.ImageConfig.MediaType)
		resp.Header().Set("Content-Length", strconv.FormatInt(int64(r.ImageConfig.Size), 10))
		resp.WriteHeader(http.StatusOK)
		return
	default:
		writeError(resp, http.StatusBadRequest, "METHOD_UNKNOWN", "We don't understand your method + url")
		return
	}
}

func writeError(resp http.ResponseWriter, status int, code, message string) {
	resp.WriteHeader(status)
	type err struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	type wrap struct {
		Errors []err `json:"errors"`
	}
	_ = json.NewEncoder(resp).Encode(wrap{
		Errors: []err{
			{
				Code:    code,
				Message: message,
			},
		},
	})
}
