package api

import (
	"context"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
)

func NewGithubActionsClient() corev1connect.GithubActionsServiceClient {
	return corev1connect.NewGithubActionsServiceClient(getHTTPClient(getBaseURL()), getBaseURL(), WithUserAgent())
}

func ListGithubActionsJobs(ctx context.Context, token, orgID string, req *corev1.ListGithubActionsJobsRequest) (*corev1.ListGithubActionsJobsResponse, error) {
	resp, err := NewGithubActionsClient().ListGithubActionsJobs(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func GetGithubActionsJob(ctx context.Context, token, orgID string, req *corev1.GetGithubActionsJobRequest) (*corev1.GetGithubActionsJobResponse, error) {
	resp, err := NewGithubActionsClient().GetGithubActionsJob(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func GetGithubActionsJobMetrics(ctx context.Context, token, orgID string, req *corev1.GetGithubActionsJobMetricsRequest) (*corev1.GetGithubActionsJobMetricsResponse, error) {
	resp, err := NewGithubActionsClient().GetGithubActionsJobMetrics(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func ListGithubActionsJobAnalytics(ctx context.Context, token, orgID string, req *corev1.ListGithubActionsJobAnalyticsRequest) (*corev1.ListGithubActionsJobAnalyticsResponse, error) {
	resp, err := NewGithubActionsClient().ListGithubActionsJobAnalytics(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}
