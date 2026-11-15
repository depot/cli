package org

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/spf13/viper"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var creditObservedAt = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

type creditHandlerFunc func(context.Context, *connect.Request[corev1.GetCreditBalanceRequest]) (*connect.Response[corev1.GetCreditBalanceResponse], error)

func (f creditHandlerFunc) GetCreditBalance(ctx context.Context, req *connect.Request[corev1.GetCreditBalanceRequest]) (*connect.Response[corev1.GetCreditBalanceResponse], error) {
	return f(ctx, req)
}

func startCreditServer(t *testing.T, handler creditHandlerFunc) {
	t.Helper()
	_, h := corev1connect.NewCreditServiceHandler(handler)
	server := httptest.NewServer(h2c.NewHandler(h, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	t.Setenv("DEPOT_TOKEN", "depot_reader_test")
}

func creditResponse(amount int64, currency string) *connect.Response[corev1.GetCreditBalanceResponse] {
	return connect.NewResponse(&corev1.GetCreditBalanceResponse{
		RemainingCreditMinor: proto.Int64(amount),
		Currency:             currency,
		ObservedAt:           timestamppb.New(creditObservedAt),
	})
}

func executeCredits(ctx context.Context, args ...string) (string, error) {
	cmd := NewCmdOrg()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(append([]string{"credits"}, args...))
	err := cmd.ExecuteContext(ctx)
	return out.String(), err
}

func TestCreditsHumanOutput(t *testing.T) {
	for _, tt := range []struct {
		currency string
		minor    int64
		want     string
	}{
		{"USD", 12500, "USD 125.00"},
		{"USD", 0, "USD 0.00"},
		{"EUR", 1, "EUR 0.01"},
		{"JPY", 1250, "JPY 1250"},
		{"KWD", 1250, "KWD 1.250"},
		{"USD", math.MaxInt64, "USD 92233720368547758.07"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			startCreditServer(t, func(context.Context, *connect.Request[corev1.GetCreditBalanceRequest]) (*connect.Response[corev1.GetCreditBalanceResponse], error) {
				return creditResponse(tt.minor, tt.currency), nil
			})
			out, err := executeCredits(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if want := "Remaining credits: " + tt.want + "\nObserved at: 2026-10-27T12:00:00Z\n"; out != want {
				t.Fatalf("output = %q, want %q", out, want)
			}
		})
	}
}

func TestCreditsJSONPreservesExactMinorUnits(t *testing.T) {
	for _, amount := range []int64{0, math.MaxInt64} {
		startCreditServer(t, func(context.Context, *connect.Request[corev1.GetCreditBalanceRequest]) (*connect.Response[corev1.GetCreditBalanceResponse], error) {
			return creditResponse(amount, "USD"), nil
		})
		out, err := executeCredits(context.Background(), "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			RemainingCreditMinor string `json:"remainingCreditMinor"`
			Currency             string `json:"currency"`
			ObservedAt           string `json:"observedAt"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("invalid JSON output: %v\n%s", err, out)
		}
		wantAmount := "0"
		if amount == math.MaxInt64 {
			wantAmount = "9223372036854775807"
		}
		if got.RemainingCreditMinor != wantAmount || got.Currency != "USD" || got.ObservedAt != "2026-10-27T12:00:00Z" {
			t.Fatalf("unexpected JSON response: %+v", got)
		}
	}
}

func TestCreditsUsesTokenWithoutOrganizationOverride(t *testing.T) {
	for _, tt := range []struct {
		name, flag, env, stored, want string
	}{
		{"flag", "explicit-token", "env-token", "stored-token", "explicit-token"},
		{"environment", "", "depot_reader_env", "stored-token", "depot_reader_env"},
		{"configuration", "", "", "stored-token", "stored-token"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := make(chan *connect.Request[corev1.GetCreditBalanceRequest], 1)
			startCreditServer(t, func(_ context.Context, req *connect.Request[corev1.GetCreditBalanceRequest]) (*connect.Response[corev1.GetCreditBalanceResponse], error) {
				requests <- req
				return creditResponse(12500, "USD"), nil
			})
			t.Setenv("DEPOT_TOKEN", tt.env)
			viper.Set("api_token", tt.stored)
			viper.Set("org_id", "other-organization")
			t.Cleanup(viper.Reset)
			out, err := executeCredits(context.Background(), "--token", tt.flag)
			if err != nil {
				t.Fatal(err)
			}
			req := <-requests
			if got := req.Header().Get("Authorization"); got != "Bearer "+tt.want {
				t.Fatalf("authorization = %q", got)
			}
			if req.Header().Get("X-Depot-Org") != "" {
				t.Fatal("current organization must not override the token's organization")
			}
			if !strings.Contains(out, "USD 125.00") || !strings.HasPrefix(req.Header().Get("User-Agent"), "depot-cli/") {
				t.Fatal("missing balance output or CLI user agent")
			}
		})
	}
}

func TestCreditsErrorsDoNotPrintBalanceOrRetry(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeResourceExhausted, connect.CodeUnavailable, connect.CodeUnauthenticated, connect.CodeFailedPrecondition, connect.CodeUnimplemented} {
		t.Run(code.String(), func(t *testing.T) {
			var calls atomic.Int32
			startCreditServer(t, func(context.Context, *connect.Request[corev1.GetCreditBalanceRequest]) (*connect.Response[corev1.GetCreditBalanceResponse], error) {
				calls.Add(1)
				err := connect.NewError(code, errors.New("test rejection"))
				if code == connect.CodeResourceExhausted {
					err.Meta().Set("Retry-After", "60")
				}
				return nil, err
			})
			out, err := executeCredits(context.Background(), "--output", "json")
			if connect.CodeOf(err) != code || out != "" || calls.Load() != 1 {
				t.Fatalf("output = %q, error = %v, requests = %d", out, err, calls.Load())
			}
			if code == connect.CodeResourceExhausted && !strings.Contains(err.Error(), "retry after 60 seconds") {
				t.Fatalf("retry delay missing: %v", err)
			}
		})
	}
}

func TestCreditsRejectsMissingCredentialsAndInvalidFlagsBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	startCreditServer(t, func(context.Context, *connect.Request[corev1.GetCreditBalanceRequest]) (*connect.Response[corev1.GetCreditBalanceResponse], error) {
		calls.Add(1)
		return creditResponse(12500, "USD"), nil
	})
	for _, key := range []string{"DEPOT_TOKEN", "DEPOT_API_TOKEN", "DEPOT_JIT_TOKEN", "DEPOT_CACHE_TOKEN"} {
		t.Setenv(key, "")
	}
	viper.Set("api_token", "")
	t.Cleanup(viper.Reset)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{nil, "missing organization API token"},
		{[]string{"--output", "csv"}, "--output must be json"},
		{[]string{"unexpected"}, "unknown command"},
		{[]string{"--org", "other-org"}, "unknown flag"},
	} {
		out, err := executeCredits(context.Background(), tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.want) || out != "" {
			t.Fatalf("args = %v, output = %q, error = %v; want %q", tt.args, out, err, tt.want)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input must not consume the polling allowance")
	}
}

func TestCreditsRejectsIncompleteBalances(t *testing.T) {
	for _, tt := range []struct {
		name   string
		modify func(*corev1.GetCreditBalanceResponse)
	}{
		{"missing amount", func(r *corev1.GetCreditBalanceResponse) { r.RemainingCreditMinor = nil }},
		{"negative amount", func(r *corev1.GetCreditBalanceResponse) { r.RemainingCreditMinor = proto.Int64(-1) }},
		{"missing currency", func(r *corev1.GetCreditBalanceResponse) { r.Currency = "" }},
		{"missing observation time", func(r *corev1.GetCreditBalanceResponse) { r.ObservedAt = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			startCreditServer(t, func(context.Context, *connect.Request[corev1.GetCreditBalanceRequest]) (*connect.Response[corev1.GetCreditBalanceResponse], error) {
				resp := creditResponse(12500, "USD")
				tt.modify(resp.Msg)
				return resp, nil
			})
			out, err := executeCredits(context.Background(), "--output", "json")
			if err == nil || !strings.Contains(err.Error(), "invalid credit balance response") || out != "" {
				t.Fatalf("output = %q, error = %v", out, err)
			}
		})
	}
}
