package api

import "github.com/depot/cli/pkg/proto/depot/core/v1/corev1connect"

// NewUsageAnalyticsClient returns the organization-scoped usage analytics client.
func NewUsageAnalyticsClient() corev1connect.UsageAnalyticsServiceClient {
	baseURL := getBaseURL()
	return corev1connect.NewUsageAnalyticsServiceClient(getHTTPClient(baseURL), baseURL, WithUserAgent())
}
