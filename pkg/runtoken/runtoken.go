// Package runtoken mints a sandbox's Depot API token from its identity: a managed run's (DEP-7956) or a Dispatch turn's (DEP-8526).
package runtoken

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// EnvURL names the sandbox endpoint that answers with a fresh JWT-SVID as {"value": "<jwt>"}.
const EnvURL = "DEPOT_RUN_TOKEN_URL"

// EnvAPIURL is the same endpoint in a Dispatch sandbox, where the token acts as the running turn's sender.
const EnvAPIURL = "DEPOT_API_TOKEN_URL"

// An SVID lives five minutes, so one this close to expiry is replaced before use.
const refreshBefore = time.Minute

type Source struct {
	env    string
	url    string
	client *http.Client
	now    func() time.Time

	mu     sync.Mutex
	token  string
	expiry time.Time
	minted map[string]struct{}
}

func New(env, url string) *Source {
	return &Source{
		env: env,
		url: url,
		// The endpoint is link-local: a configured proxy cannot reach it. It never redirects, so none is followed.
		client: &http.Client{
			Timeout:       15 * time.Second,
			Transport:     &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now:    time.Now,
		minted: map[string]struct{}{},
	}
}

var (
	sourcesMu sync.Mutex
	sources   = map[string]*Source{}
)

// fromEnv keeps one Source per variable for as long as its value stays the same, so Minted sees every token it issued.
func fromEnv(env string) *Source {
	url := os.Getenv(env)
	if url == "" {
		return nil
	}
	sourcesMu.Lock()
	defer sourcesMu.Unlock()
	if source := sources[env]; source != nil && source.url == url {
		return source
	}
	sources[env] = New(env, url)
	return sources[env]
}

// Default is the Source named by DEPOT_RUN_TOKEN_URL, or nil outside a managed run.
func Default() *Source {
	return fromEnv(EnvURL)
}

// Turn is the Source named by DEPOT_API_TOKEN_URL, or nil outside a Dispatch sandbox. The API takes its token on a few session RPCs only.
func Turn() *Source {
	return fromEnv(EnvAPIURL)
}

// Token returns the cached token, minting a new one when it is near expiry.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && s.now().Add(refreshBefore).Before(s.expiry) {
		return s.token, nil
	}

	token, err := s.fetch(ctx)
	if err != nil {
		// A failed refresh keeps a token that has not expired yet.
		if s.token != "" && s.now().Before(s.expiry) {
			return s.token, nil
		}
		return "", err
	}
	s.token, s.expiry = token, expiry(token)
	s.minted[token] = struct{}{}
	return token, nil
}

// Minted reports whether token came from this Source, so callers replace only tokens it owns.
func (s *Source) Minted(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.minted[token]
	return ok
}

// fetch GETs the URL as given. Its errors name the variable and never carry the URL, the response body or a token.
func (s *Source) fetch(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return "", fmt.Errorf("%s is not a valid URL", s.env)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", s.failed(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", s.failed(err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("could not fetch a token from %s: the endpoint answered HTTP %d", s.env, resp.StatusCode)
	}

	var out struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Value == "" {
		return "", fmt.Errorf("could not fetch a token from %s: the answer has no token", s.env)
	}
	return out.Value, nil
}

// fetchError says why a fetch failed without the transport error's text, which can quote the URL or the response.
type fetchError struct {
	env, reason string
	cause       error
	timeout     bool
}

func (e *fetchError) Error() string {
	return fmt.Sprintf("could not fetch a token from %s: %s", e.env, e.reason)
}
func (e *fetchError) Unwrap() error { return e.cause }
func (e *fetchError) Timeout() bool { return e.timeout }

func (s *Source) failed(err error) error {
	var netErr net.Error
	var opErr *net.OpError
	switch {
	case errors.Is(err, context.Canceled):
		return &fetchError{env: s.env, reason: "canceled", cause: context.Canceled}
	case errors.Is(err, context.DeadlineExceeded):
		return &fetchError{env: s.env, reason: "the request timed out", cause: context.DeadlineExceeded, timeout: true}
	case errors.As(err, &netErr) && netErr.Timeout():
		return &fetchError{env: s.env, reason: "the request timed out", timeout: true}
	case errors.As(err, &opErr):
		return &fetchError{env: s.env, reason: "could not connect"}
	}
	return &fetchError{env: s.env, reason: "the request failed"}
}

// expiry reads the JWT's exp claim unverified; a token without one is treated as already expiring.
func expiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

// Transport swaps a Bearer token this Source minted for a current one on every request.
type Transport struct {
	Source *Source
	Base   http.RoundTripper
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok || !t.Source.Minted(token) {
		return t.Base.RoundTrip(req)
	}

	fresh, err := t.Source.Token(req.Context())
	if err != nil {
		return nil, err
	}
	if fresh != token {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+fresh)
	}
	return t.Base.RoundTrip(req)
}

// WrapClient returns client with token refresh for each sandbox token URL that is set, or client itself when none is.
func WrapClient(client *http.Client) *http.Client {
	for _, source := range []*Source{Default(), Turn()} {
		if source == nil {
			continue
		}
		base := client.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		wrapped := *client
		wrapped.Transport = &Transport{Source: source, Base: base}
		client = &wrapped
	}
	return client
}
