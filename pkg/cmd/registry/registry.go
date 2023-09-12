package registry

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/defaults"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
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

	client, err := NewContainerdClient(ctx, caCert, certPEM, keyPEM, string(addr))
	if err != nil {
		return err
	}
	defer client.Close()

	registry := NewRegistry(rawConfig, rawManifest, manifest, client.ContentStore())
	srv.Handler = registry
	srv.Addr = ":8888"

	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}

	<-shutdown
	return nil
}

func NewContainerdClient(ctx context.Context, caCert, certPEM, keyPEM []byte, buildkitdAddress string) (*containerd.Client, error) {
	uri, err := url.Parse(buildkitdAddress)
	if err != nil {
		return nil, err
	}

	certPool := x509.NewCertPool()
	if ok := certPool.AppendCertsFromPEM(caCert); !ok {
		return nil, fmt.Errorf("failed to append ca certs")
	}

	cfg := &tls.Config{RootCAs: certPool}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("could not read certificate/key: %w", err)
	}
	cfg.Certificates = []tls.Certificate{cert}

	opts := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(defaults.DefaultMaxRecvMsgSize)),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(defaults.DefaultMaxSendMsgSize)),
		grpc.WithAuthority(uri.Host),
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			addr := strings.TrimPrefix(buildkitdAddress, "tcp://")
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		}),
		grpc.FailOnNonTempDialError(true),
		grpc.WithReturnConnectionError(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time: 10 * time.Second,
		}),
		// grpc.WithReadBufferSize(1024 * 1024),
		// grpc.WithWriteBufferSize(1024 * 1024),
		grpc.WithInitialWindowSize(1 << 30),
		grpc.WithInitialConnWindowSize(1 << 30),
	}

	conn, err := grpc.DialContext(ctx, buildkitdAddress, opts...)
	if err != nil {
		return nil, err
	}

	return containerd.NewWithConn(conn)
}

// Registry is a small docker registry serving a single image by forwarding requests to the BuildKit cache.
type Registry struct {
	RawConfig    []byte
	ConfigDigest Digest

	RawManifest    []byte
	ManifestDigest Digest
	Manifest       Manifest

	ContentStore content.Store
}

func NewRegistry(rawConfig, rawManifest []byte, manifest Manifest, store content.Store) *Registry {
	return &Registry{
		RawConfig:      rawConfig,
		ConfigDigest:   FromBytes(rawConfig),
		RawManifest:    rawManifest,
		ManifestDigest: FromBytes(rawManifest),
		Manifest:       manifest,
		ContentStore:   store,
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

	ra, err := r.ContentStore.ReaderAt(req.Context(), v1.Descriptor{Digest: digest.Digest(blobSHA)})
	if err != nil {
		writeError(resp, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "unable to get blob")
		return
	}
	defer ra.Close()

	cr := readerOnly{readerSpy{content.NewReader(readerAtSpy{ra})}}

	// respWriter := writerOnly{newFlushWriter(resp)}
	respWriter := bufio.NewWriter(writerOnly{newFlushWriter(resp)})

	start := time.Now()

	buf := make([]byte, 4*1024*1024)
	written, err := copyBuffer(respWriter, cr, buf)
	// written, err := io.Copy(resp, cr)
	if err != nil {
		log.Printf("unable to read %s: %v", blobSHA, err)
		return
	}
	if respWriter.Buffered() > 0 {
		fmt.Println("Bufferred", respWriter.Buffered())
		respWriter.Flush()
	}
	throughput := float64(written) / time.Since(start).Seconds()
	log.Printf("Sent %d bytes in %s (%.2f MB/s)", written, time.Since(start), throughput/1024/1024)
}

type writerOnly struct {
	io.Writer
}

type readerOnly struct {
	io.Reader
}

type flushWriter struct {
	f http.Flusher
	w io.Writer
}

func newFlushWriter(w io.Writer) io.Writer {
	f, ok := w.(http.Flusher)
	if !ok {
		log.Printf("Not a flush writer")
		return w
	}
	log.Printf("Using flush writer")
	return &flushWriter{f: f, w: w}
}

func (fw *flushWriter) Write(bs []byte) (int, error) {
	log.Printf("Writing %d bytes", len(bs))
	n, err := fw.w.Write(bs)
	if err != nil {
		return n, err
	}
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, nil
}

type readerSpy struct {
	r io.Reader
}

func (rs readerSpy) Read(bs []byte) (int, error) {
	n, err := rs.r.Read(bs)
	log.Printf("[spy] Read %d: read %d bytes (err %v)", len(bs), n, err)
	return n, err
}

type readerAtSpy struct {
	ra content.ReaderAt
}

func (rs readerAtSpy) ReadAt(bs []byte, off int64) (int, error) {
	n, err := rs.ra.ReadAt(bs, off)
	log.Printf("[spy] ReadAt %d %d: read %d bytes (err %v)", len(bs), off, n, err)
	return n, err
}

func (rs readerAtSpy) Close() error {
	log.Printf("[spy] Close")
	return rs.ra.Close()
}

func (rs readerAtSpy) Size() int64 {
	log.Printf("[spy] Size")
	return rs.ra.Size()
}

func copyBuffer(dst io.Writer, src io.Reader, buf []byte) (written int64, err error) {
	// // If the reader has a WriteTo method, use it to do the copy.
	// // Avoids an allocation and a copy.
	// if wt, ok := src.(io.WriterTo); ok {
	// 	log.Printf("Using WriteTo")
	// 	return wt.WriteTo(dst)
	// }
	// // Similarly, if the writer has a ReadFrom method, use it to do the copy.
	// if rt, ok := dst.(io.ReaderFrom); ok {
	// 	log.Printf("Using ReadFrom")
	// 	return rt.ReadFrom(src)
	// }
	if buf == nil {
		size := 32 * 1024
		if l, ok := src.(*io.LimitedReader); ok && int64(size) > l.N {
			if l.N < 1 {
				size = 1
			} else {
				size = int(l.N)
			}
		}
		buf = make([]byte, size)
	}
	for {
		nr, er := src.Read(buf)
		log.Printf("Read %d bytes", nr)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errors.New("invalid write")
				}
			}
			written += int64(nw)
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return written, err
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
