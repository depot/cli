package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	corev1 "github.com/depot/cli/pkg/proto/depot/core/v1"
	"github.com/depot/cli/pkg/proto/depot/core/v1/corev1connect"
)

func TestOrganizationTokensClientDoesNotFollowRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, destination := range []string{"same origin", "same host downgrade", "different host"} {
			t.Run(fmt.Sprintf("%d/%s", status, destination), func(t *testing.T) {
				var originRequests, redirectedRequests atomic.Int32
				var authenticated atomic.Bool
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					redirectedRequests.Add(1)
					w.Header().Set("Content-Type", "application/proto")
				}))
				t.Cleanup(target.Close)

				redirectURL := target.URL
				if destination == "different host" {
					redirectURL = strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
				}
				origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/redirected" {
						redirectedRequests.Add(1)
						w.Header().Set("Content-Type", "application/proto")
						return
					}
					originRequests.Add(1)
					authenticated.Store(r.Header.Get("Authorization") == "Bearer owner-user-token" && r.Header.Get("x-depot-org") == "org-1")
					http.Redirect(w, r, redirectURL, status)
				}))
				t.Cleanup(origin.Close)
				if destination == "same origin" {
					redirectURL = origin.URL + "/redirected"
				}

				// Trust this local server and dial TLS directly so the racing TCP dialer
				// does not leave an unused connection awaiting a TLS handshake.
				transport := origin.Client().Transport.(*http.Transport).Clone()
				transport.DialTLSContext = (&tls.Dialer{Config: transport.TLSClientConfig}).DialContext
				originalTransport := http.DefaultTransport
				http.DefaultTransport = transport
				t.Cleanup(func() {
					http.DefaultTransport = originalTransport
					transport.CloseIdleConnections()
				})
				t.Setenv("DEPOT_API_URL", origin.URL)

				_, err := NewOrganizationTokensClient().CreateToken(context.Background(), WithAuthenticationAndOrg(
					connect.NewRequest(&corev1.CreateOrganizationTokenRequest{Description: "CI builds"}), "owner-user-token", "org-1",
				))
				if got := redirectedRequests.Load(); got != 0 {
					t.Fatalf("credential-management request followed a redirect %d times", got)
				}
				if err == nil {
					t.Fatal("redirect must fail token creation")
				}
				if originRequests.Load() != 1 || !authenticated.Load() {
					t.Fatalf("expected one authenticated request at the selected API: requests=%d authenticated=%v", originRequests.Load(), authenticated.Load())
				}
			})
		}
	}
}

func TestOrganizationTokensClientPreservesDefaultClientRedirectPolicy(t *testing.T) {
	var redirects atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirects.Add(1)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)

	// Exercise the fallback for custom transports without changing its client.
	originalClient, originalTransport := http.DefaultClient, http.DefaultTransport
	http.DefaultClient = server.Client()
	http.DefaultTransport = organizationTokenTestTransport{http.DefaultClient.Transport}
	t.Cleanup(func() {
		http.DefaultClient, http.DefaultTransport = originalClient, originalTransport
	})
	t.Setenv("DEPOT_API_URL", server.URL)

	_, err := NewOrganizationTokensClient().CreateToken(context.Background(), WithAuthenticationAndOrg(
		connect.NewRequest(&corev1.CreateOrganizationTokenRequest{Description: "CI builds"}), "owner-user-token", "org-1",
	))
	if err == nil || redirects.Load() != 0 {
		t.Fatalf("custom transport followed token redirect: redirects=%d err=%v", redirects.Load(), err)
	}
	response, err := http.DefaultClient.Get(server.URL + corev1connect.OrganizationTokenServiceCreateTokenProcedure)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if redirects.Load() != 1 {
		t.Fatal("token client changed the shared default client's redirect policy")
	}
}

type organizationTokenTestTransport struct{ http.RoundTripper }
