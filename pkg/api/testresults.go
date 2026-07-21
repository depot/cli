package api

import (
	"context"

	"connectrpc.com/connect"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/depot/cli/pkg/proto/depot/testresults/v1/testresultsv1connect"
)

func newTestResultsServiceClient() testresultsv1connect.TestResultsServiceClient {
	baseURL := baseURLFunc()
	return testresultsv1connect.NewTestResultsServiceClient(getHTTPClient(baseURL), baseURL, WithUserAgent())
}

func ListTestResults(ctx context.Context, token, orgID string, req *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
	client := newTestResultsServiceClient()
	resp, err := client.ListTestResults(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func ReportTestResults(ctx context.Context, token string, req *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
	client := newTestResultsServiceClient()
	resp, err := client.ReportTestResults(ctx, WithAuthentication(connect.NewRequest(req), token))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func SplitTests(ctx context.Context, token string, req *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
	client := newTestResultsServiceClient()
	resp, err := client.SplitTests(ctx, WithAuthentication(connect.NewRequest(req), token))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}
