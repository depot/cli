// Package runtoken mints the Depot API token of a Depot managed run from its sandbox identity (DEP-7956).
package runtoken

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// EnvURL names the sandbox endpoint that answers with a fresh JWT-SVID as {"value": "<jwt>"}.
const EnvURL = "DEPOT_RUN_TOKEN_URL"

// An SVID lives five minutes, so one this close to expiry is replaced before use.
const refreshBefore = time.Minute

type Source struct {
	url    string
	client *http.Client
	now    func() time.Time

	mu     sync.Mutex
	token  string
	expiry time.Time
	minted map[string]struct{}
}

func New(url string) *Source {
	return &Source{
		url: url,
		// The endpoint is link-local: a configured proxy cannot reach it.
		client: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}},
		now:    time.Now,
		minted: map[string]struct{}{},
	}
}

var defaultSource = sync.OnceValue(func() *Source {
	if url := os.Getenv(EnvURL); url != "" {
		return New(url)
	}
	return nil
})

// Default is the Source named by DEPOT_RUN_TOKEN_URL, or nil outside a managed run.
func Default() *Source {
	return defaultSource()
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

func (s *Source) fetch(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch run token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("read run token: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch run token: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode run token: %w", err)
	}
	if out.Value == "" {
		return "", fmt.Errorf("fetch run token: empty token")
	}
	return out.Value, nil
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

// WrapClient returns client with run token refresh, or client itself outside a managed run.
func WrapClient(client *http.Client) *http.Client {
	source := Default()
	if source == nil {
		return client
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	wrapped := *client
	wrapped.Transport = &Transport{Source: source, Base: base}
	return &wrapped
}
