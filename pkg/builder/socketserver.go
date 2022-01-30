package builder

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// socketProxyServer represents a server that proxies a local unix socket to a remote builder
type socketProxyServer struct {
	computeHost string
	apiKey      string
	builderID   string
	socketPath  string
}

func newSocketProxyServer(computeHost string, apiKey string, builderID string) (*socketProxyServer, error) {
	socketFile, err := os.CreateTemp("", "depot-builder-*.sock")
	if err != nil {
		return nil, err
	}
	socketPath := socketFile.Name()
	socketFile.Close()
	os.Remove(socketPath)

	return &socketProxyServer{
		computeHost: computeHost,
		apiKey:      apiKey,
		builderID:   builderID,
		socketPath:  socketPath,
	}, nil
}

func (s *socketProxyServer) Listen(onListening chan<- error) error {
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	onListening <- nil

	builderURL, err := url.Parse(s.computeHost + "/" + s.builderID)
	if err != nil {
		return err
	}
	proxy := newSingleHostReverseProxy(builderURL, s.apiKey)

	h2s := &http2.Server{}
	h1s := &http.Server{Handler: h2c.NewHandler(proxy, h2s)}

	return h1s.Serve(listener)
}

func newSingleHostReverseProxy(target *url.URL, apiKey string) *httputil.ReverseProxy {
	targetQuery := target.RawQuery
	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		req.URL.Path, req.URL.RawPath = joinURLPath(target, req.URL)
		if targetQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = targetQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
		}
		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
		req.Header.Set("Authorization", "bearer "+apiKey)
		req.Header.Set("Host", target.Host)
	}
	return &httputil.ReverseProxy{Director: director}
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

func joinURLPath(a, b *url.URL) (path, rawpath string) {
	if a.RawPath == "" && b.RawPath == "" {
		return singleJoiningSlash(a.Path, b.Path), ""
	}
	// Same as singleJoiningSlash, but uses EscapedPath to determine
	// whether a slash should be added
	apath := a.EscapedPath()
	bpath := b.EscapedPath()

	aslash := strings.HasSuffix(apath, "/")
	bslash := strings.HasPrefix(bpath, "/")

	switch {
	case aslash && bslash:
		return a.Path + b.Path[1:], apath + bpath[1:]
	case !aslash && !bslash:
		return a.Path + "/" + b.Path, apath + "/" + bpath
	}
	return a.Path + b.Path, apath + bpath
}
