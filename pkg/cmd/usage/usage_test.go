package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	usagev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type usageHandler struct {
	corev1connect.UnimplementedUsageAnalyticsServiceHandler
	workflow func(context.Context, *connect.Request[usagev1.ListDepotCIUsageByWorkflowRequest]) (*connect.Response[usagev1.ListDepotCIUsageByWorkflowResponse], error)
	series   func(context.Context, *connect.Request[usagev1.GetOrganizationUsageSeriesRequest]) (*connect.Response[usagev1.GetOrganizationUsageSeriesResponse], error)
}

func (h usageHandler) ListDepotCIUsageByWorkflow(ctx context.Context, req *connect.Request[usagev1.ListDepotCIUsageByWorkflowRequest]) (*connect.Response[usagev1.ListDepotCIUsageByWorkflowResponse], error) {
	return h.workflow(ctx, req)
}
func (h usageHandler) GetOrganizationUsageSeries(ctx context.Context, req *connect.Request[usagev1.GetOrganizationUsageSeriesRequest]) (*connect.Response[usagev1.GetOrganizationUsageSeriesResponse], error) {
	return h.series(ctx, req)
}
func server(t *testing.T, h usageHandler) {
	t.Helper()
	_, handler := corev1connect.NewUsageAnalyticsServiceHandler(h)
	srv := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(srv.Close)
	t.Setenv("DEPOT_API_URL", srv.URL)
	t.Setenv("DEPOT_TOKEN", "reader-token")
}
func execute(args ...string) (string, error) {
	cmd := NewCmdUsage()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}
func TestWorkflowUsageRoundTrip(t *testing.T) {
	server(t, usageHandler{workflow: func(_ context.Context, req *connect.Request[usagev1.ListDepotCIUsageByWorkflowRequest]) (*connect.Response[usagev1.ListDepotCIUsageByWorkflowResponse], error) {
		if req.Header().Get("Authorization") != "Bearer reader-token" || req.Header().Get("x-depot-org") != "org-a" {
			t.Errorf("missing scoped credentials")
		}
		if req.Msg.GetPageToken() != "next-page" || req.Msg.GetPageSize() != 1 || strings.Join(req.Msg.Repositories, ",") != "owner/repo" || strings.Join(req.Msg.WorkflowKeys, ",") != "ci" {
			t.Errorf("unexpected query: %v", req.Msg)
		}
		if req.Msg.TimeRange.StartAt.AsTime().Format("2006-01-02") != "2026-10-01" {
			t.Errorf("wrong period: %v", req.Msg.TimeRange)
		}
		return connect.NewResponse(&usagev1.ListDepotCIUsageByWorkflowResponse{Workflows: []*usagev1.DepotCIWorkflowUsage{{Repository: "owner/repo", WorkflowKey: "ci", AttemptCount: 9007199254740993, VcpuSeconds: 120}}, NextPageToken: "more"}), nil
	}})
	out, err := execute("breakdown", "--product", "depot-ci", "--group-by", "workflow", "--repo", "owner/repo", "--workflow-key", "ci", "--page-size", "1", "--page-token", "next-page", "--org", "org-a", "--start", "2026-10-01T00:00:00Z", "--end", "2026-10-08T00:00:00Z", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Workflows []struct {
			AttemptCount string  `json:"attemptCount"`
			VcpuSeconds  float64 `json:"vcpuSeconds"`
		} `json:"workflows"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Workflows) != 1 || result.Workflows[0].AttemptCount != "9007199254740993" || result.Workflows[0].VcpuSeconds != 120 || result.NextPageToken != "more" {
		t.Fatalf("unexpected JSON: %s", out)
	}
}
func TestSeriesCarriesProductSelectionAndCoverage(t *testing.T) {
	server(t, usageHandler{series: func(_ context.Context, req *connect.Request[usagev1.GetOrganizationUsageSeriesRequest]) (*connect.Response[usagev1.GetOrganizationUsageSeriesResponse], error) {
		if strings.Join(req.Msg.Products, ",") != "depot-ci,storage" {
			t.Errorf("wrong products: %v", req.Msg.Products)
		}
		return connect.NewResponse(&usagev1.GetOrganizationUsageSeriesResponse{Coverage: []*usagev1.UsageAnalyticsCoverage{{Product: "depot-ci", Unavailable: true}}}), nil
	}})
	out, err := execute("series", "--product", "depot-ci", "--product", "storage")
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	coverage := result["coverage"].([]any)[0].(map[string]any)
	if coverage["unavailable"] != true {
		t.Fatalf("lost coverage: %s", out)
	}
}
func TestInvalidQueriesFailBeforeNetworkOrOutput(t *testing.T) {
	for _, args := range [][]string{
		{"series", "--start", "2026-10-01T00:00:00Z"},
		{"series", "--start", "invalid", "--end", "2026-10-08T00:00:00Z"},
		{"series", "--start", "2026-10-08T00:00:00Z", "--end", "2026-10-01T00:00:00Z"},
		{"series", "--product", "typo"},
		{"series", "--project", "project-id", "--product", "depot-ci"},
		{"breakdown", "--product", "container-builds", "--group-by", "project", "--repo", "owner/repo"},
		{"breakdown", "--product", "depot-ci", "--group-by", "workflow", "--workflow-path", "ci.yml"},
		{"breakdown", "--product", "depot-ci", "--group-by", "workflow", "--page-size", "501"},
		{"breakdown", "--product", "depot-ci", "--group-by", "project"},
	} {
		out, err := execute(args...)
		if err == nil || out != "" {
			t.Errorf("%v: output=%q error=%v", args, out, err)
		}
	}
}
func TestUnsupportedServerFailsWithoutPartialJSON(t *testing.T) {
	server(t, usageHandler{})
	out, err := execute("report")
	if connect.CodeOf(err) != connect.CodeUnimplemented || out != "" {
		t.Fatalf("output=%q error=%v", out, err)
	}
}
