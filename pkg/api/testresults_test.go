package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/depot/cli/pkg/proto/depot/testresults/v1/testresultsv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func TestListTestResultsSendsAuthAndOrgHeaders(t *testing.T) {
	handler := &testResultsHandler{}
	mux := http.NewServeMux()
	path, connectHandler := testresultsv1connect.NewTestResultsServiceHandler(handler)
	mux.Handle(path, connectHandler)
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)

	previousBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = previousBaseURLFunc })

	resp, err := ListTestResults(
		context.Background(),
		"token-1",
		"org-1",
		&testresultsv1.ListTestResultsRequest{
			OwnerType: testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_CI,
			OwnerId:   "attempt-1",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if resp.GetOwnerId() != "attempt-1" {
		t.Fatalf("expected owner ID %q, got %q", "attempt-1", resp.GetOwnerId())
	}
	if handler.authorization != "Bearer token-1" {
		t.Fatalf("expected auth header %q, got %q", "Bearer token-1", handler.authorization)
	}
	if handler.orgID != "org-1" {
		t.Fatalf("expected org header %q, got %q", "org-1", handler.orgID)
	}
	if handler.request.GetOwnerId() != "attempt-1" {
		t.Fatalf("expected request owner ID %q, got %q", "attempt-1", handler.request.GetOwnerId())
	}
}

type testResultsHandler struct {
	testresultsv1connect.UnimplementedTestResultsServiceHandler

	authorization string
	orgID         string
	request       *testresultsv1.ListTestResultsRequest
}

func (h *testResultsHandler) ListTestResults(
	_ context.Context,
	req *connect.Request[testresultsv1.ListTestResultsRequest],
) (*connect.Response[testresultsv1.ListTestResultsResponse], error) {
	h.authorization = req.Header().Get("Authorization")
	h.orgID = req.Header().Get("x-depot-org")
	h.request = req.Msg
	return connect.NewResponse(&testresultsv1.ListTestResultsResponse{
		OwnerType: req.Msg.GetOwnerType(),
		OwnerId:   req.Msg.GetOwnerId(),
	}), nil
}
