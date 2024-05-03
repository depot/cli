package registry

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	contentv1 "github.com/containerd/containerd/api/services/content/v1"
	"github.com/containerd/containerd/defaults"
	"github.com/opencontainers/go-digest"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

func NewCmdRegistry() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "registry",
		Short:  "Run a local registry",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run()
		},
	}

	return cmd
}

func run() error {
	caCert, err := base64.StdEncoding.DecodeString(os.Getenv("CA_CERT"))
	if err != nil {
		return err
	}

	keyPEM, err := base64.StdEncoding.DecodeString(os.Getenv("KEY"))
	if err != nil {
		return err
	}

	certPEM, err := base64.StdEncoding.DecodeString(os.Getenv("CERT"))
	if err != nil {
		return err
	}

	addr, err := base64.StdEncoding.DecodeString(os.Getenv("ADDR"))
	if err != nil {
		return err
	}

	serverName, err := base64.StdEncoding.DecodeString(os.Getenv("SERVER_NAME"))
	if err != nil {
		return err
	}

	rawConfig, err := base64.StdEncoding.DecodeString(os.Getenv("CONFIG"))
	if err != nil {
		return err
	}

	rawManifest, err := base64.StdEncoding.DecodeString(os.Getenv("MANIFEST"))
	if err != nil {
		return err
	}

	var manifest Manifest
	err = json.Unmarshal(rawManifest, &manifest)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", ":8888")
	if err != nil {
		return err
	}

	var srv http.Server

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdown := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt)
		<-sigint

		if err := srv.Shutdown(context.Background()); err != nil {
			log.Printf("HTTP server shutdown: %v", err)
		}
		close(shutdown)
		cancel()
	}()

	contentClient, err := NewContentClient(ctx, caCert, certPEM, keyPEM, string(serverName), string(addr))
	if err != nil {
		return err
	}

	registry := NewRegistry(rawConfig, rawManifest, manifest, contentClient)
	srv.Handler = registry
	srv.Addr = ":8888"

	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}

	<-shutdown
	return nil
}

func NewContentClient(ctx context.Context, caCert, certPEM, keyPEM []byte, serverName, buildkitdAddress string) (contentv1.ContentClient, error) {
	certPool := x509.NewCertPool()
	if ok := certPool.AppendCertsFromPEM(caCert); !ok {
		return nil, fmt.Errorf("failed to append ca certs")
	}

	cfg := &tls.Config{RootCAs: certPool, ServerName: serverName}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("could not read certificate/key: %w", err)
	}
	cfg.Certificates = []tls.Certificate{cert}

	opts := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(defaults.DefaultMaxRecvMsgSize)),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(defaults.DefaultMaxSendMsgSize)),
		grpc.WithAuthority(serverName),
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			addr := strings.TrimPrefix(buildkitdAddress, "tcp://")
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		}),
		grpc.FailOnNonTempDialError(true),
		grpc.WithReturnConnectionError(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 10 * time.Second}),
	}

	conn, err := grpc.DialContext(ctx, buildkitdAddress, opts...)
	if err != nil {
		return nil, err
	}

	return contentv1.NewContentClient(conn), nil
}

// Registry is a small docker registry serving a single image by forwarding requests to the BuildKit cache.
type Registry struct {
	RawConfig    []byte
	ConfigDigest Digest

	RawManifest    []byte
	ManifestDigest Digest
	Manifest       Manifest

	ContentClient contentv1.ContentClient
}

func NewRegistry(rawConfig, rawManifest []byte, manifest Manifest, contentClient contentv1.ContentClient) *Registry {
	return &Registry{
		RawConfig:      rawConfig,
		ConfigDigest:   FromBytes(rawConfig),
		RawManifest:    rawManifest,
		ManifestDigest: FromBytes(rawManifest),
		Manifest:       manifest,
		ContentClient:  contentClient,
	}
}

func (r *Registry) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	if isConfig(req, r.ConfigDigest) {
		r.handleConfig(resp, req)
		return
	}

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
	log.Printf("Healthy")
}

func isManifest(req *http.Request) bool {
	elems := strings.Split(req.URL.Path, "/")
	elems = elems[1:]
	if len(elems) < 4 {
		return false
	}
	return elems[len(elems)-2] == "manifests"
}

func (r *Registry) handleManifests(resp http.ResponseWriter, req *http.Request) {
	resp.Header().Set("Content-Length", strconv.FormatInt(int64(len(r.RawManifest)), 10))
	resp.Header().Set("Docker-Content-Digest", r.ManifestDigest.String())
	resp.Header().Set("Content-Type", r.Manifest.MediaType)

	log.Printf("Manifest")
	if req.Method == http.MethodGet {
		_, _ = io.Copy(resp, bytes.NewReader(r.RawManifest))
	}
}

func isConfig(req *http.Request, config Digest) bool {
	return strings.HasSuffix(req.URL.Path, config.String())
}

func (r *Registry) handleConfig(resp http.ResponseWriter, req *http.Request) {
	resp.Header().Set("Content-Length", strconv.FormatInt(int64(len(r.RawConfig)), 10))
	resp.Header().Set("Docker-Content-Digest", r.ConfigDigest.String())

	log.Printf("Config")
	if req.Method == http.MethodGet {
		_, _ = io.Copy(resp, bytes.NewReader(r.RawConfig))
	}
}

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
	blobSHA := elem[len(elem)-1]

	var found bool
	for _, layer := range r.Manifest.Layers {
		if layer.Digest.String() == blobSHA {
			resp.Header().Set("Content-Length", strconv.FormatInt(layer.Size, 10))
			resp.Header().Set("Docker-Content-Digest", layer.Digest.String())
			found = true
		}
	}

	if !found {
		log.Printf("Unknown blob: %s", blobSHA)
		writeError(resp, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
		return
	}

	log.Printf("%s Blob: %s", req.Method, blobSHA)

	if req.Method != http.MethodGet {
		return
	}

	childCtx, cancel := context.WithCancel(req.Context())
	defer cancel()

	rc, err := r.ContentClient.Read(childCtx, &contentv1.ReadContentRequest{Digest: digest.Digest(blobSHA)})
	if err != nil {
		writeError(resp, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "unable to get blob")
		return
	}

	type chunk struct {
		Data []byte
		Err  error
	}

	chunks := make(chan chunk, 8)

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()

		for {
			resp, err := rc.Recv()
			if err != nil {
				chunks <- chunk{nil, err}
				close(chunks)
				break
			} else {
				chunks <- chunk{resp.Data, nil}
			}
		}
	}()

	written := 0

	wg.Add(1)
	go func() {
		defer wg.Done()

		bodyWritten := false

		for chunk := range chunks {
			if chunk.Err != nil {
				if chunk.Err != io.EOF {
					log.Printf("Error receiving chunk: %v", chunk.Err)
					if !bodyWritten {
						writeError(resp, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "unable to get blob")
					}
				}
				return
			}

			chunkWritten, err := resp.Write(chunk.Data)
			if err != nil {
				log.Printf("Error writing chunk: %v", err)
				return
			}
			bodyWritten = true

			written += chunkWritten
		}
	}()

	wg.Wait()
}

func writeError(resp http.ResponseWriter, status int, code, message string) {
	log.Printf("Error: %s: %s", code, message)
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

type Digest string

func FromBytes(bs []byte) Digest {
	hash := crypto.SHA256.New()
	_, _ = hash.Write(bs)
	return Digest(fmt.Sprintf("sha256:%x", hash.Sum(nil)))
}

func (d Digest) String() string {
	return string(d)
}

// Manifest provides `application/vnd.oci.image.manifest.v1+json` mediatype structure when marshalled to JSON.
type Manifest struct {
	SchemaVersion int `json:"schemaVersion"`

	// MediaType specificies the type of this document data structure e.g. `application/vnd.oci.image.manifest.v1+json`
	MediaType string `json:"mediaType,omitempty"`

	// Config references a configuration object for a container, by digest.
	// The referenced configuration object is a JSON blob that the runtime uses to set up the container.
	Config Descriptor `json:"config"`

	// Layers is an indexed list of layers referenced by the manifest.
	Layers []Descriptor `json:"layers"`

	// Annotations contains arbitrary metadata for the image manifest.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Descriptor describes the disposition of targeted content.
// This structure provides `application/vnd.oci.descriptor.v1+json` mediatype
// when marshalled to JSON.
type Descriptor struct {
	// MediaType is the media type of the object this schema refers to.
	MediaType string `json:"mediaType,omitempty"`

	// Digest is the digest of the targeted content.
	Digest Digest `json:"digest"`

	// Size specifies the size in bytes of the blob.
	Size int64 `json:"size"`

	// URLs specifies a list of URLs from which this object MAY be downloaded
	URLs []string `json:"urls,omitempty"`

	// Annotations contains arbitrary metadata relating to the targeted content.
	Annotations map[string]string `json:"annotations,omitempty"`

	// Platform describes the platform which the image in the manifest runs on.
	//
	// This should only be used when referring to a manifest.
	Platform *Platform `json:"platform,omitempty"`
}

type Platform struct {
	// Architecture field specifies the CPU architecture, for example
	// `amd64` or `ppc64`.
	Architecture string `json:"architecture"`

	// OS specifies the operating system, for example `linux` or `windows`.
	OS string `json:"os"`

	// OSVersion is an optional field specifying the operating system
	// version, for example on Windows `10.0.14393.1066`.
	OSVersion string `json:"os.version,omitempty"`

	// OSFeatures is an optional field specifying an array of strings,
	// each listing a required OS feature (for example on Windows `win32k`).
	OSFeatures []string `json:"os.features,omitempty"`

	// Variant is an optional field specifying a variant of the CPU, for
	// example `v7` to specify ARMv7 when architecture is `arm`.
	Variant string `json:"variant,omitempty"`
}
