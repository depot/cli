package buildkite

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEndpoint  = "https://agent.buildkite.com/"
	defaultUserAgent = "buildkite-agent/api"
)

// Config is configuration for the API Client
type Config struct {
	// Endpoint for API requests. Defaults to the public Buildkite Agent API.
	// The URL should always be specified with a trailing slash.
	Endpoint string

	// The authentication token to use, either a registration or access token
	Token string

	// User agent used when communicating with the Buildkite Agent API.
	UserAgent string

	// If true, only HTTP2 is disabled
	DisableHTTP2 bool

	// If true, requests and responses will be dumped and set to the logger
	DebugHTTP bool

	// The http client used, leave nil for the default
	HTTPClient *http.Client
}

// A Client manages communication with the Buildkite Agent API.
type Client struct {
	// The client configuration
	conf Config

	// HTTP client used to communicate with the API.
	client *http.Client
}

// NewClient returns a new Buildkite Agent API Client.
func NewClient(conf Config) *Client {
	if conf.Endpoint == "" {
		conf.Endpoint = defaultEndpoint
	}

	if conf.UserAgent == "" {
		conf.UserAgent = defaultUserAgent
	}

	httpClient := conf.HTTPClient
	if conf.HTTPClient == nil {
		t := &http.Transport{
			Proxy:              http.ProxyFromEnvironment,
			DisableCompression: false,
			DisableKeepAlives:  false,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:        100,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 30 * time.Second,
		}

		if conf.DisableHTTP2 {
			t.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
		}

		httpClient = &http.Client{
			Timeout: 60 * time.Second,
			Transport: &authenticatedTransport{
				Token:    conf.Token,
				Delegate: t,
			},
		}
	}

	return &Client{
		client: httpClient,
		conf:   conf,
	}
}

// Config returns the internal configuration for the Client
func (c *Client) Config() Config {
	return c.conf
}

type Header struct {
	Name  string
	Value string
}

// NewRequest creates an API request. A relative URL can be provided in urlStr,
// in which case it is resolved relative to the BaseURL of the Client.
// Relative URLs should always be specified without a preceding slash. If
// specified, the value pointed to by body is JSON encoded and included as the
// request body.
func (c *Client) newRequest(
	ctx context.Context,
	method, urlStr string,
	body any,
	headers ...Header,
) (*http.Request, error) {
	u := joinURLPath(c.conf.Endpoint, urlStr)

	buf := new(bytes.Buffer)
	if body != nil {
		err := json.NewEncoder(buf).Encode(body)
		if err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, u, buf)
	if err != nil {
		return nil, err
	}

	req.Header.Add("User-Agent", c.conf.UserAgent)

	// If our context has a timeout/deadline, tell the server how long is remaining.
	// This may allow the server to configure its own timeouts accordingly.
	if deadline, ok := ctx.Deadline(); ok {
		ms := time.Until(deadline).Milliseconds()
		if ms > 0 {
			req.Header.Add("Buildkite-Timeout-Milliseconds", strconv.FormatInt(ms, 10))
		}
	}

	for _, header := range headers {
		req.Header.Add(header.Name, header.Value)
	}

	if body != nil {
		req.Header.Add("Content-Type", "application/json")
	}

	return req, nil
}

// Response is a Buildkite Agent API response. This wraps the standard
// http.Response.
type Response struct {
	*http.Response
}

// newResponse creates a new Response for the provided http.Response.
func newResponse(r *http.Response) *Response {
	response := &Response{Response: r}
	return response
}

// Do sends an API request and returns the API response. The API response is
// JSON decoded and stored in the value pointed to by v, or returned as an
// error if an API error has occurred.  If v implements the io.Writer
// interface, the raw response body will be written to v, without attempting to
// first decode it.
func (c *Client) doRequest(req *http.Request, v any) (*Response, error) {
	var err error

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
	}()

	response := newResponse(resp)

	err = checkResponse(resp)
	if err != nil {
		// even though there was an error, we still return the response
		// in case the caller wants to inspect it further
		return response, err
	}

	if v != nil {
		if w, ok := v.(io.Writer); ok {
			_, _ = io.Copy(w, resp.Body)
		} else {
			if strings.Contains(req.Header.Get("Content-Type"), "application/msgpack") {
				err = errors.New("Msgpack not supported")
			} else {
				err = json.NewDecoder(resp.Body).Decode(v)
			}
		}
	}

	return response, err
}

// ErrorResponse provides a message.
type ErrorResponse struct {
	Response *http.Response // HTTP response that caused this error
	Message  string         `json:"message"` // error message
}

func (r *ErrorResponse) Error() string {
	s := fmt.Sprintf("%v %v: %s",
		r.Response.Request.Method, r.Response.Request.URL,
		r.Response.Status)

	if r.Message != "" {
		s = fmt.Sprintf("%s: %v", s, r.Message)
	}

	return s
}

func IsErrHavingStatus(err error, code int) bool {
	var apierr *ErrorResponse
	return errors.As(err, &apierr) && apierr.Response.StatusCode == code
}

func checkResponse(r *http.Response) error {
	if c := r.StatusCode; 200 <= c && c <= 299 {
		return nil
	}

	errorResponse := &ErrorResponse{Response: r}
	data, err := io.ReadAll(r.Body)
	if err == nil && data != nil {
		_ = json.Unmarshal(data, errorResponse)
	}

	return errorResponse
}

func joinURLPath(endpoint string, path string) string {
	return strings.TrimRight(endpoint, "/") + "/" + strings.TrimLeft(path, "/")
}
