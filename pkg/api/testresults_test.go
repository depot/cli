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
	setupTestResultsServer(t, handler)

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

func TestSplitTestsSendsAuthHeader(t *testing.T) {
	handler := &testResultsHandler{}
	setupTestResultsServer(t, handler)

	resp, err := SplitTests(
		context.Background(),
		"oidc-token-1",
		&testresultsv1.SplitTestsRequest{
			Candidates:        []string{"a.test.ts", "b.test.ts"},
			CandidateIdentity: testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME,
			TimingIdentity:    testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_TESTNAME,
			ShardIndex:        1,
			ShardTotal:        2,
			SplitKey:          "unit",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if handler.authorization != "Bearer oidc-token-1" {
		t.Fatalf("expected auth header %q, got %q", "Bearer oidc-token-1", handler.authorization)
	}
	if handler.splitRequest.GetShardIndex() != 1 || handler.splitRequest.GetShardTotal() != 2 {
		t.Fatalf("expected shard 1/2, got %d/%d", handler.splitRequest.GetShardIndex(), handler.splitRequest.GetShardTotal())
	}
	if handler.splitRequest.GetCandidateIdentity() != testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME {
		t.Fatalf("expected filename candidate identity, got %v", handler.splitRequest.GetCandidateIdentity())
	}
	if handler.splitRequest.GetTimingIdentity() != testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_TESTNAME {
		t.Fatalf("expected testname timing identity, got %v", handler.splitRequest.GetTimingIdentity())
	}
	if handler.splitRequest.GetSplitKey() != "unit" {
		t.Fatalf("expected split key %q, got %q", "unit", handler.splitRequest.GetSplitKey())
	}
	if got := resp.GetCandidates(); len(got) != 1 || got[0] != "b.test.ts" {
		t.Fatalf("expected selected candidate b.test.ts, got %v", got)
	}
}

func TestReportTestResultsSendsAuthHeader(t *testing.T) {
	handler := &testResultsHandler{}
	setupTestResultsServer(t, handler)

	resp, err := ReportTestResults(
		context.Background(),
		"oidc-token-1",
		&testresultsv1.ReportTestResultsRequest{
			InvocationId: "unit",
			Files: []*testresultsv1.TestResultsFile{
				{Filename: "junit.xml", GzippedXml: []byte{0x1f, 0x8b}},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if handler.authorization != "Bearer oidc-token-1" {
		t.Fatalf("expected auth header %q, got %q", "Bearer oidc-token-1", handler.authorization)
	}
	if handler.reportRequest.GetInvocationId() != "unit" {
		t.Fatalf("expected invocation ID %q, got %q", "unit", handler.reportRequest.GetInvocationId())
	}
	if len(handler.reportRequest.GetFiles()) != 1 || handler.reportRequest.GetFiles()[0].GetFilename() != "junit.xml" {
		t.Fatalf("expected junit.xml report file, got %v", handler.reportRequest.GetFiles())
	}
	if resp.GetFilesProcessed() != 1 || resp.GetTestsReported() != 2 {
		t.Fatalf("expected report response counts, got files=%d tests=%d", resp.GetFilesProcessed(), resp.GetTestsReported())
	}
}

func setupTestResultsServer(t *testing.T, handler *testResultsHandler) {
	t.Helper()

	mux := http.NewServeMux()
	path, connectHandler := testresultsv1connect.NewTestResultsServiceHandler(handler)
	mux.Handle(path, connectHandler)
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)

	previousBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	t.Cleanup(func() { baseURLFunc = previousBaseURLFunc })
}

type testResultsHandler struct {
	testresultsv1connect.UnimplementedTestResultsServiceHandler

	authorization string
	orgID         string
	request       *testresultsv1.ListTestResultsRequest
	splitRequest  *testresultsv1.SplitTestsRequest
	reportRequest *testresultsv1.ReportTestResultsRequest
}

func (h *testResultsHandler) ReportTestResults(
	_ context.Context,
	req *connect.Request[testresultsv1.ReportTestResultsRequest],
) (*connect.Response[testresultsv1.ReportTestResultsResponse], error) {
	h.authorization = req.Header().Get("Authorization")
	h.orgID = req.Header().Get("x-depot-org")
	h.reportRequest = req.Msg
	return connect.NewResponse(&testresultsv1.ReportTestResultsResponse{
		FilesProcessed: 1,
		TestsReported:  2,
	}), nil
}

func (h *testResultsHandler) SplitTests(
	_ context.Context,
	req *connect.Request[testresultsv1.SplitTestsRequest],
) (*connect.Response[testresultsv1.SplitTestsResponse], error) {
	h.authorization = req.Header().Get("Authorization")
	h.orgID = req.Header().Get("x-depot-org")
	h.splitRequest = req.Msg
	return connect.NewResponse(&testresultsv1.SplitTestsResponse{
		Candidates:            []string{"b.test.ts"},
		CandidatesRequested:   uint32(len(req.Msg.GetCandidates())),
		CandidatesSelected:    1,
		CandidatesWithTimings: 1,
	}), nil
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
