package runtoken

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	source := New(EnvURL, server.URL+"/token?audience=managed-agent-run-api")
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
	source := New(EnvURL, server.URL)
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
	source := New(EnvURL, server.URL)
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

func TestTokenErrorsNameTheVariableAndLeakNothing(t *testing.T) {
	var status int
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status == http.StatusFound {
			w.Header().Set("Location", "http://url-secret\x7f/")
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	for _, tc := range []struct {
		name, url, body string
		status          int
	}{
		{name: "error status", url: server.URL + "/token?k=url-secret", status: http.StatusServiceUnavailable, body: "body-secret"},
		{name: "wrong shape", url: server.URL + "/token?k=url-secret", status: http.StatusOK, body: `{"token":"body-secret"}`},
		{name: "not JSON", url: server.URL + "/token?k=url-secret", status: http.StatusOK, body: "body-secret"},
		{name: "redirect", url: server.URL + "/token?k=url-secret", status: http.StatusFound},
		{name: "unreachable", url: closed.URL + "/token?k=url-secret"},
		{name: "invalid URL", url: "http://url-secret\x7f/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body = tc.status, tc.body
			token, err := New(EnvAPIURL, tc.url).Token(context.Background())
			if err == nil || token != "" {
				t.Fatalf("got %q, %v; want an error", token, err)
			}
			if msg := err.Error(); !strings.Contains(msg, EnvAPIURL) || strings.Contains(msg, "secret") {
				t.Fatalf("error must name %s and carry neither the URL nor the body: %q", EnvAPIURL, msg)
			}
		})
	}
}

func TestEachVariableHasItsOwnSourceAndFollowsTheEnvironment(t *testing.T) {
	t.Setenv(EnvURL, "")
	t.Setenv(EnvAPIURL, "")
	if Default() != nil || Turn() != nil {
		t.Fatal("no variable set must mean no source")
	}
	t.Setenv(EnvAPIURL, "http://api")
	turn := Turn()
	if Default() != nil || turn == nil || turn.env != EnvAPIURL {
		t.Fatalf("DEPOT_API_TOKEN_URL must feed Turn only, got %+v", turn)
	}
	if Turn() != turn {
		t.Fatal("an unchanged variable must keep its source, or Minted forgets its tokens")
	}
	t.Setenv(EnvURL, "http://run")
	if run := Default(); run == nil || run.env != EnvURL || Turn() != turn {
		t.Fatalf("DEPOT_RUN_TOKEN_URL must feed Default only, got %+v", run)
	}
	t.Setenv(EnvAPIURL, "")
	if Turn() != nil {
		t.Fatal("clearing the variable must drop the source")
	}
}

func TestFetchErrorKeepsCancellationAndTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(EnvAPIURL, server.URL).Token(ctx)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("a canceled fetch must stay canceled and hide the URL: %v", err)
	}

	source := New(EnvAPIURL, server.URL)
	source.client.Timeout = 20 * time.Millisecond
	_, err = source.Token(context.Background())
	var timeout interface{ Timeout() bool }
	if !errors.As(err, &timeout) || !timeout.Timeout() || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("a timed out fetch must report Timeout and hide the URL: %v", err)
	}
}

// The endpoint answers for the first audience parameter only, so the URL is requested exactly as given.
func TestFetchSendsTheURLAsGiven(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RequestURI()
		fmt.Fprint(w, `{"value":"svid"}`)
	}))
	defer server.Close()
	if token, err := New(EnvAPIURL, server.URL+"/token?audience=depot-api").Token(context.Background()); err != nil || token != "svid" {
		t.Fatalf("got %q, %v", token, err)
	}
	if got != "/token?audience=depot-api" {
		t.Fatalf("requested %q", got)
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
	t.Setenv(EnvURL, "")
	t.Setenv(EnvAPIURL, "")
	client := &http.Client{}
	if WrapClient(client) != client {
		t.Fatal("outside a sandbox the client must be returned as is")
	}
}

func TestWrapClientRefreshesTheTurnToken(t *testing.T) {
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"value":"turn-svid"}`)
	}))
	defer tokens.Close()
	t.Setenv(EnvURL, "")
	t.Setenv(EnvAPIURL, tokens.URL)
	if _, err := Turn().Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	transport, ok := WrapClient(&http.Client{}).Transport.(*Transport)
	if !ok || transport.Source != Turn() || !transport.Source.Minted("turn-svid") {
		t.Fatalf("the client must refresh tokens from %s, got %+v", EnvAPIURL, transport)
	}
}
