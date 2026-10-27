package runtoken

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func jwt(exp time.Time, n int64) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"n":%d}`, exp.Unix(), n)))
	return "e30." + payload + ".sig"
}

// identityServer stands in for the sandbox endpoint, minting a new five-minute SVID per call.
func identityServer(t *testing.T, now func() time.Time) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("audience") != "managed-agent-run-api" {
			http.Error(w, "missing audience parameter", http.StatusBadRequest)
			return
		}
		n := calls.Add(1)
		fmt.Fprintf(w, `{"value":%q}`, jwt(now().Add(5*time.Minute), n))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func newTestSource(t *testing.T) (*Source, *time.Time, *atomic.Int64) {
	t.Helper()
	clock := time.Unix(1_800_000_000, 0)
	now := func() time.Time { return clock }
	server, calls := identityServer(t, now)
	source := New(server.URL + "/token?audience=managed-agent-run-api")
	source.now = now
	return source, &clock, calls
}

func TestTokenCachesUntilNearExpiry(t *testing.T) {
	source, clock, calls := newTestSource(t)
	ctx := context.Background()

	first, err := source.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(3 * time.Minute)
	if again, _ := source.Token(ctx); again != first || calls.Load() != 1 {
		t.Fatalf("expected cached token at 3m, got %d fetches", calls.Load())
	}

	*clock = clock.Add(90 * time.Second)
	refreshed, err := source.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed == first || calls.Load() != 2 {
		t.Fatalf("expected a fresh token within a minute of expiry, got %d fetches", calls.Load())
	}
	if !source.Minted(first) || !source.Minted(refreshed) || source.Minted("user-token") {
		t.Fatal("Minted must recognise exactly the tokens this source issued")
	}
}

func TestTokenWithoutExpiryIsRefetched(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"value":"opaque"}`)
	}))
	defer server.Close()
	source := New(server.URL)
	for range 2 {
		if token, err := source.Token(context.Background()); err != nil || token != "opaque" {
			t.Fatalf("got %q, %v", token, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("expected a fetch per call, got %d", calls.Load())
	}
}

func TestFailedRefreshKeepsUnexpiredToken(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			http.Error(w, "no identity", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"value":%q}`, jwt(clock.Add(5*time.Minute), 1))
	}))
	defer server.Close()
	source := New(server.URL)
	source.now = func() time.Time { return clock }

	first, err := source.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	clock = clock.Add(4*time.Minute + 30*time.Second)
	if token, err := source.Token(context.Background()); err != nil || token != first {
		t.Fatalf("inside the refresh window a failed fetch should keep the token, got %q, %v", token, err)
	}
	clock = clock.Add(time.Minute)
	if _, err := source.Token(context.Background()); err == nil {
		t.Fatal("an expired token must not be returned when the refresh fails")
	}
}

func TestTokenErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no identity", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if _, err := New(server.URL).Token(context.Background()); err == nil {
		t.Fatal("expected an error from a failing endpoint")
	}
}

func TestTransportRefreshesMintedTokenPerRequest(t *testing.T) {
	source, clock, _ := newTestSource(t)
	var seen []string
	api := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
	}))
	defer api.Close()
	client := &http.Client{Transport: &Transport{Source: source, Base: http.DefaultTransport}}

	// The command resolves its token once at startup, as every caller does today.
	startup, err := source.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	get := func(token string) {
		req, _ := http.NewRequest(http.MethodGet, api.URL, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	get(startup)
	*clock = clock.Add(10 * time.Minute)
	get(startup)
	get("explicit-token")

	if seen[0] != "Bearer "+startup {
		t.Fatalf("first request should carry the startup token, got %q", seen[0])
	}
	if seen[1] == "Bearer "+startup || !source.Minted(seen[1][len("Bearer "):]) {
		t.Fatalf("request after expiry should carry a fresh token, got %q", seen[1])
	}
	if seen[2] != "Bearer explicit-token" {
		t.Fatalf("a token the source did not mint must pass through, got %q", seen[2])
	}
}

func TestWrapClientWithoutEnvIsUnchanged(t *testing.T) {
	if Default() != nil {
		t.Skip(EnvURL + " is set in this environment")
	}
	client := &http.Client{}
	if WrapClient(client) != client {
		t.Fatal("outside a managed run the client must be returned as is")
	}
}
