package builder

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// proxyServer represents a server that proxies a remote builder
type proxyServer struct {
	server *httptest.Server
}

func newProxyServer(computeHost string, apiKey string, builderID string) (*proxyServer, error) {
	builderURL, err := url.Parse(computeHost + "/" + builderID)
	if err != nil {
		return nil, err
	}
	proxy := newSingleHostReverseProxy(builderURL, apiKey)

	h2s := &http2.Server{}
	handler := h2c.NewHandler(proxy, h2s)

	server := httptest.NewUnstartedServer(handler)

	return &proxyServer{
		server: server,
	}, nil
}

func (s *proxyServer) Addr() net.Addr {
	return s.server.Listener.Addr()
}

func (s *proxyServer) Close() {
	s.server.Close()
}

func (s *proxyServer) Start() {
	s.server.Start()
}

func newSingleHostReverseProxy(target *url.URL, apiKey string) *httputil.ReverseProxy {
	targetQuery := target.RawQuery
	director := func(req *http.Request) {

		// NOTE: we need to set req.Host, which the regular httputil.NewSingleHostReverseProxy does not do
		req.Host = target.Host

		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
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
