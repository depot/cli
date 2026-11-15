package org

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/spf13/viper"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type invoiceTestHandler struct {
	corev1connect.UnimplementedBillingServiceHandler
	list  func(*corev1.ListInvoicesRequest) (*corev1.ListInvoicesResponse, error)
	get   func(*corev1.GetInvoiceRequest) (*corev1.GetInvoiceResponse, error)
	lines func(*corev1.ListInvoiceLineItemsRequest) (*corev1.ListInvoiceLineItemsResponse, error)
}

func (h invoiceTestHandler) ListInvoices(_ context.Context, r *connect.Request[corev1.ListInvoicesRequest]) (*connect.Response[corev1.ListInvoicesResponse], error) {
	if h.list == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unexpected list"))
	}
	msg, err := h.list(r.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(msg), nil
}
func (h invoiceTestHandler) GetInvoice(_ context.Context, r *connect.Request[corev1.GetInvoiceRequest]) (*connect.Response[corev1.GetInvoiceResponse], error) {
	if h.get == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unexpected get"))
	}
	msg, err := h.get(r.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(msg), nil
}
func (h invoiceTestHandler) ListInvoiceLineItems(_ context.Context, r *connect.Request[corev1.ListInvoiceLineItemsRequest]) (*connect.Response[corev1.ListInvoiceLineItemsResponse], error) {
	if h.lines == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unexpected lines"))
	}
	msg, err := h.lines(r.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(msg), nil
}

func startInvoiceServer(t *testing.T, handler invoiceTestHandler, token, org string) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	_, h := corev1connect.NewBillingServiceHandler(handler)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Depot-Org") != org {
			t.Error("incorrect authentication or organization selection")
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "depot-cli/") {
			t.Error("missing CLI user agent")
		}
		h.ServeHTTP(w, r)
	}), &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
	t.Setenv("DEPOT_TOKEN", token)
	return &calls
}

func executeInvoices(ctx context.Context, args ...string) (string, error) {
	cmd := NewCmdOrg()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(append([]string{"invoices"}, args...))
	err := cmd.ExecuteContext(ctx)
	return out.String(), err
}

func TestInvoicesJSONPreservesHistoryAndPagination(t *testing.T) {
	standalone := &corev1.Invoice{Id: "in_standalone", Status: "paid", Currency: "usd", Total: 9007199254740993, StartingBalance: -1500, SubtotalExcludingTax: proto.Int64(0)}
	previous := &corev1.Invoice{Id: "in_previous", SubscriptionId: "sub_canceled"}
	want := &corev1.ListInvoicesResponse{Invoices: []*corev1.Invoice{standalone, previous}, NextPageToken: "next-page"}
	startInvoiceServer(t, invoiceTestHandler{list: func(req *corev1.ListInvoicesRequest) (*corev1.ListInvoicesResponse, error) {
		if req.PageSize != 2 || req.PageToken != "signed-page" {
			t.Errorf("request = %v", req)
		}
		return want, nil
	}}, "org-token", "")
	viper.Set("org_id", "unrelated-org")
	t.Cleanup(viper.Reset)
	out, err := executeInvoices(context.Background(), "list", "--page-size", "2", "--page-token", "signed-page")
	if err != nil {
		t.Fatal(err)
	}
	got := &corev1.ListInvoicesResponse{}
	if err := protojson.Unmarshal([]byte(out), got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, want) || !strings.Contains(out, `"9007199254740993"`) || !strings.Contains(out, `"-1500"`) {
		t.Fatalf("invoice fields or amount precision lost: %s", out)
	}
}

func TestInvoicesEmptyPageRetainsNextToken(t *testing.T) {
	calls := startInvoiceServer(t, invoiceTestHandler{list: func(req *corev1.ListInvoicesRequest) (*corev1.ListInvoicesResponse, error) {
		if req.PageToken == "" {
			return &corev1.ListInvoicesResponse{NextPageToken: "past-drafts"}, nil
		}
		if req.PageToken != "past-drafts" {
			t.Errorf("token = %q", req.PageToken)
		}
		return &corev1.ListInvoicesResponse{Invoices: []*corev1.Invoice{{Id: "in_after_drafts"}}}, nil
	}}, "org-token", "")
	out, err := executeInvoices(context.Background(), "list")
	if err != nil {
		t.Fatal(err)
	}
	first := &corev1.ListInvoicesResponse{}
	if err := protojson.Unmarshal([]byte(out), first); err != nil {
		t.Fatal(err)
	}
	if len(first.Invoices) != 0 || first.NextPageToken != "past-drafts" || calls.Load() != 1 {
		t.Fatalf("unexpected first page: %s", out)
	}
	out, err = executeInvoices(context.Background(), "list", "--page-token", first.NextPageToken)
	if err != nil {
		t.Fatal(err)
	}
	second := &corev1.ListInvoicesResponse{}
	if err := protojson.Unmarshal([]byte(out), second); err != nil {
		t.Fatal(err)
	}
	if len(second.Invoices) != 1 || second.Invoices[0].Id != "in_after_drafts" || second.NextPageToken != "" || calls.Load() != 2 {
		t.Fatalf("unexpected second page: %s", out)
	}
}

func TestInvoicesDetailsAndLinesWithOwnerToken(t *testing.T) {
	invoice := &corev1.Invoice{Id: "in_one", PdfUrl: proto.String("https://invoice.example.test/pdf")}
	lines := &corev1.ListInvoiceLineItemsResponse{LineItems: []*corev1.InvoiceLineItem{{Id: "il_one", Amount: -9007199254740993, Quantity: proto.Int64(0), UnitAmountDecimal: proto.String("0.0001")}}, NextPageToken: "more-lines"}
	startInvoiceServer(t, invoiceTestHandler{
		get: func(req *corev1.GetInvoiceRequest) (*corev1.GetInvoiceResponse, error) {
			if req.InvoiceId != invoice.Id {
				t.Errorf("invoice ID = %q", req.InvoiceId)
			}
			return &corev1.GetInvoiceResponse{Invoice: invoice}, nil
		},
		lines: func(req *corev1.ListInvoiceLineItemsRequest) (*corev1.ListInvoiceLineItemsResponse, error) {
			if req.InvoiceId != invoice.Id || req.PageSize != 1 || req.PageToken != "line-page" {
				t.Errorf("line request = %v", req)
			}
			return lines, nil
		},
	}, "owner-token", "org-a")
	t.Setenv("DEPOT_TOKEN", "ignored-token")
	out, err := executeInvoices(context.Background(), "get", invoice.Id, "--token", "owner-token", "--org", "org-a")
	if err != nil {
		t.Fatal(err)
	}
	got := &corev1.GetInvoiceResponse{}
	if err := protojson.Unmarshal([]byte(out), got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got.Invoice, invoice) {
		t.Fatalf("details lost: %s", out)
	}
	out, err = executeInvoices(context.Background(), "line-items", invoice.Id, "--page-size", "1", "--page-token", "line-page", "--token", "owner-token", "--org", "org-a")
	if err != nil {
		t.Fatal(err)
	}
	gotLines := &corev1.ListInvoiceLineItemsResponse{}
	if err := protojson.Unmarshal([]byte(out), gotLines); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(gotLines, lines) {
		t.Fatalf("line values lost: %s", out)
	}
}

func TestInvoiceErrorsDoNotPrintDataOrRetry(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeNotFound, connect.CodePermissionDenied, connect.CodeUnavailable, connect.CodeInvalidArgument, connect.CodeUnimplemented} {
		t.Run(code.String(), func(t *testing.T) {
			failure := connect.NewError(code, errors.New("Invoice request rejected"))
			calls := startInvoiceServer(t, invoiceTestHandler{
				list: func(*corev1.ListInvoicesRequest) (*corev1.ListInvoicesResponse, error) { return nil, failure },
				get:  func(*corev1.GetInvoiceRequest) (*corev1.GetInvoiceResponse, error) { return nil, failure },
				lines: func(*corev1.ListInvoiceLineItemsRequest) (*corev1.ListInvoiceLineItemsResponse, error) {
					return nil, failure
				},
			}, "org-token", "")
			for _, args := range [][]string{{"list"}, {"get", "in_missing"}, {"lines", "in_missing"}} {
				out, err := executeInvoices(context.Background(), args...)
				if out != "" || connect.CodeOf(err) != code {
					t.Fatalf("output=%q error=%v", out, err)
				}
			}
			if calls.Load() != 3 {
				t.Fatalf("unexpected retry: %d requests", calls.Load())
			}
		})
	}
}

func TestInvoicesInvalidInputMakesNoRequests(t *testing.T) {
	calls := startInvoiceServer(t, invoiceTestHandler{}, "org-token", "")
	for _, args := range [][]string{{"list", "--page-size", "0"}, {"list", "--page-size", "101"}, {"lines", "in_one", "--page-size", "-1"}, {"lines", "in_one", "--page-size", "101"}, {"get"}, {"get", "in_one", "extra"}, {"list", "--output", "csv"}} {
		out, err := executeInvoices(context.Background(), args...)
		if err == nil || out != "" {
			t.Fatalf("args=%v output=%q error=%v", args, out, err)
		}
	}
	for _, key := range []string{"DEPOT_TOKEN", "DEPOT_API_TOKEN", "DEPOT_JIT_TOKEN", "DEPOT_CACHE_TOKEN"} {
		t.Setenv(key, "")
	}
	viper.Set("api_token", "")
	t.Cleanup(viper.Reset)
	out, err := executeInvoices(context.Background(), "list")
	if err == nil || !strings.Contains(err.Error(), "missing API token") || out != "" {
		t.Fatalf("missing credentials: output=%q error=%v", out, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid input sent %d requests", calls.Load())
	}
}
