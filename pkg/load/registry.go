package load

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	"github.com/docker/buildx/util/progress"
	"github.com/getsentry/sentry-go"
	"github.com/opencontainers/go-digest"
)

// Registry is a small docker registry that serves a single image by loading
// blobs from a buildkitd cache.
type Registry struct {
	Client contentapi.ContentClient

	RawConfig    []byte
	ConfigDigest digest.Digest

	RawManifest    []byte
	ManifestDigest digest.Digest

	Logger progress.SubLogger
}

func NewRegistry(client contentapi.ContentClient, rawConfig, rawManifest []byte, logger progress.SubLogger) *Registry {
	return &Registry{
		Client:         client,
		RawConfig:      rawConfig,
		ConfigDigest:   digest.FromBytes(rawConfig),
		RawManifest:    rawManifest,
		ManifestDigest: digest.FromBytes(rawManifest),
		Logger:         logger,
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

	// If the request SHA is the config digest, use the in-memory cached version
	// as the config may be GCed from buildkitd.
	if theSHA == r.ConfigDigest.String() {
		resp.Header().Set("Content-Length", strconv.FormatInt(int64(len(r.RawConfig)), 10))
		resp.Header().Set("Docker-Content-Digest", r.ConfigDigest.String())
		if req.Method == http.MethodGet {
			if _, err := resp.Write(r.RawConfig); err != nil {
				_ = r.Logger.Wrap(fmt.Sprintf("[registry] unable to write %s", theSHA), func() error { return err })
			}
		}

		return
	}

	layer, err := r.Client.Info(req.Context(), &contentapi.InfoRequest{Digest: digest.Digest(theSHA)})
	if err != nil {
		_ = r.Logger.Wrap(fmt.Sprintf("[registry] layer not found %s", theSHA), func() error { return err })

		sentry.ConfigureScope(func(scope *sentry.Scope) {
			scope.SetContext("registry_blob_info", map[string]interface{}{
				"digest":   digest.Digest(theSHA).String(),
				"manifest": string(r.RawManifest),
			})
		})
		_ = sentry.CaptureException(err)

		writeError(resp, http.StatusNotFound, "BLOB_UNKNOWN", "Unknown blob")
		return
	}

	resp.Header().Set("Content-Length", strconv.FormatInt(layer.Info.Size_, 10))
	resp.Header().Set("Docker-Content-Digest", layer.Info.Digest.String())

	if req.Method == http.MethodGet {
		rr := &contentapi.ReadContentRequest{
			Digest: digest.Digest(theSHA),
		}

		childCtx, cancel := context.WithCancel(req.Context())
		defer cancel()
		rc, err := r.Client.Read(childCtx, rr)
		if err != nil {
			writeError(resp, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "Unable to read content from registry")
			return
		}

		for {
			res, err := rc.Recv()
			if err == io.EOF {
				break
			}

			if err != nil {
				_ = r.Logger.Wrap(fmt.Sprintf("[registry] unable to read %s", theSHA), func() error { return err })
				sentry.ConfigureScope(func(scope *sentry.Scope) {
					scope.SetContext("registry_blob_read", map[string]interface{}{
						"digest":   digest.Digest(theSHA).String(),
						"manifest": string(r.RawManifest),
					})
				})
				_ = sentry.CaptureException(err)

				return
			}
			_, err = resp.Write(res.Data)
			if err != nil {
				_ = r.Logger.Wrap(fmt.Sprintf("[registry] unable to write %s", theSHA), func() error { return err })
				return
			}
		}
	}
}

// https://github.com/opencontainers/distribution-spec/blob/master/spec.md#pulling-an-image-manifest
// https://github.com/opencontainers/distribution-spec/blob/master/spec.md#pushing-an-image
func (r *Registry) handleManifests(resp http.ResponseWriter, req *http.Request) {
	resp.Header().Set("Content-Length", strconv.FormatInt(int64(len(r.RawManifest)), 10))
	resp.Header().Set("Docker-Content-Digest", r.ManifestDigest.String())
	resp.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")

	resp.WriteHeader(http.StatusOK)

	if req.Method == http.MethodGet {
		_, _ = io.Copy(resp, bytes.NewReader(r.RawManifest))
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
