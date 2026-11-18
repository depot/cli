package api

import (
	"context"
	"time"

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
	retryDelays := [...]time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second, 5 * time.Second}
	for attempt := 0; ; attempt++ {
		resp, err := client.ReportTestResults(ctx, WithAuthentication(connect.NewRequest(req), token))
		if err == nil {
			return resp.Msg, nil
		}
		// GitHub job ownership can lag the OIDC token; NotFound is returned before report ingestion.
		if connect.CodeOf(err) != connect.CodeNotFound {
			return nil, err
		}
		timer := time.NewTimer(retryDelays[min(attempt, len(retryDelays)-1)])
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func SplitTests(ctx context.Context, token string, req *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
	client := newTestResultsServiceClient()
	resp, err := client.SplitTests(ctx, WithAuthentication(connect.NewRequest(req), token))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func ListTestAnalytics(ctx context.Context, token, orgID string, req *testresultsv1.ListTestAnalyticsRequest) (*testresultsv1.ListTestAnalyticsResponse, error) {
	client := newTestResultsServiceClient()
	resp, err := client.ListTestAnalytics(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}
