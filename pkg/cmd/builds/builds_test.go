package builds

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type metricsServer struct {
	corev1connect.UnimplementedBuildServiceHandler
	list      func(context.Context, *connect.Request[corev1.ListBuildMetricsRequest]) (*connect.Response[corev1.ListBuildMetricsResponse], error)
	analytics func(context.Context, *connect.Request[corev1.GetProjectBuildMetricsRequest]) (*connect.Response[corev1.GetProjectBuildMetricsResponse], error)
}

func (s *metricsServer) ListBuildMetrics(ctx context.Context, r *connect.Request[corev1.ListBuildMetricsRequest]) (*connect.Response[corev1.ListBuildMetricsResponse], error) {
	return s.list(ctx, r)
}
func (s *metricsServer) GetProjectBuildMetrics(ctx context.Context, r *connect.Request[corev1.GetProjectBuildMetricsRequest]) (*connect.Response[corev1.GetProjectBuildMetricsResponse], error) {
	return s.analytics(ctx, r)
}

func serve(t *testing.T, handler *metricsServer) {
	t.Helper()
	_, h := corev1connect.NewBuildServiceHandler(handler)
	s := httptest.NewServer(h2c.NewHandler(h, &http2.Server{}))
	t.Cleanup(s.Close)
	t.Setenv("DEPOT_API_URL", s.URL)
	t.Setenv("DEPOT_TOKEN", "org-test")
	t.Setenv("DEPOT_PROJECT_ID", "project-test")
}

var rangeArgs = []string{"--start", "2026-10-01T00:00:00Z", "--end", "2026-10-02T00:00:00Z"}

func execute(ctx context.Context, verb string, args ...string) (string, string, error) {
	cmd := NewCmdBuilds()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(append(append([]string{verb}, rangeArgs...), args...))
	var out, diagnostic bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostic)
	err := cmd.ExecuteContext(ctx)
	return out.String(), diagnostic.String(), err
}

func measured() *corev1.BuildResourceStatistics {
	return &corev1.BuildResourceStatistics{
		CpuSampleCount: 2, AverageCpuUtilization: proto.Float64(0), PeakCpuUtilization: proto.Float64(.5),
		CpuObservedSeconds: 1.5, CpuTimeSeconds: proto.Float64(3.75),
		MemorySampleCount: 3, AverageMemoryUtilization: proto.Float64(.125), PeakMemoryUtilization: proto.Float64(.25),
		MemoryObservedSeconds: 2.75, AverageMemoryBytes: proto.Float64(1024.5), PeakMemoryBytes: proto.Float64(2048),
	}
}

func samplePage() *corev1.ListBuildMetricsResponse {
	return &corev1.ListBuildMetricsResponse{
		Builds: []*corev1.BuildResourceMetrics{
			{BuildId: "build-1", Hardware: "aws-16x32", FinishedAt: timestamppb.New(time.Date(2026, 10, 1, 1, 0, 0, 123000000, time.UTC)), Machines: []*corev1.BuildMachineMetrics{
				{MachineId: "machine-1", Architecture: "amd64", CpuCapacity: proto.Uint32(16), MemoryCapacityBytes: proto.Float64(32000000000), Statistics: measured()},
				{MachineId: "machine-2", Architecture: "arm64", Statistics: &corev1.BuildResourceStatistics{}},
			}},
			{BuildId: "build-no-machine"},
		},
		NextPageToken: "next-page",
	}
}

func TestMetricsJSONPreservesMeasurementsAndPagination(t *testing.T) {
	want := samplePage()
	var calls atomic.Int32
	serve(t, &metricsServer{list: func(_ context.Context, req *connect.Request[corev1.ListBuildMetricsRequest]) (*connect.Response[corev1.ListBuildMetricsResponse], error) {
		calls.Add(1)
		if req.Header().Get("Authorization") != "Bearer explicit-token" || req.Header().Get("X-Depot-Org") != "" {
			t.Error("the explicit organization token must determine tenancy")
		}
		if req.Msg.ProjectId != "explicit-project" || req.Msg.PageSize != 7 || req.Msg.PageToken != "previous-page" || req.Msg.StartAt.AsTime().Format(time.RFC3339) != rangeArgs[1] || req.Msg.EndAt.AsTime().Format(time.RFC3339) != rangeArgs[3] {
			t.Errorf("wrong project or pagination window: %v", req.Msg)
		}
		return connect.NewResponse(want), nil
	}})
	out, stderr, err := execute(context.Background(), "metrics", "--output", "json", "--project", "explicit-project", "--token", "explicit-token", "--page-size", "7", "--page-token", "previous-page")
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.EqualValues(t, 1, calls.Load(), "never fetch the next page implicitly")
	var got corev1.ListBuildMetricsResponse
	require.NoError(t, protojson.Unmarshal([]byte(out), &got))
	require.True(t, proto.Equal(want, &got))
	var document struct {
		Builds []struct {
			Machines []struct{ Statistics map[string]json.RawMessage }
		}
	}
	require.NoError(t, json.Unmarshal([]byte(out), &document))
	require.JSONEq(t, "0", string(document.Builds[0].Machines[0].Statistics["averageCpuUtilization"]))
	require.NotContains(t, document.Builds[0].Machines[1].Statistics, "averageCpuUtilization")
}

func TestMetricsCSVAndTableRetainMissingBuildsAndCoverage(t *testing.T) {
	serve(t, &metricsServer{list: func(_ context.Context, req *connect.Request[corev1.ListBuildMetricsRequest]) (*connect.Response[corev1.ListBuildMetricsResponse], error) {
		if req.Msg.ProjectId != "project-test" || req.Header().Get("Authorization") != "Bearer org-test" {
			t.Error("environment project/token were not used")
		}
		return connect.NewResponse(samplePage()), nil
	}})
	for _, format := range []string{"csv", "table"} {
		t.Run(format, func(t *testing.T) {
			out, stderr, err := execute(context.Background(), "metrics", "--output", format)
			require.NoError(t, err)
			require.Contains(t, stderr, "next-page")
			require.NotContains(t, out, "next-page")
			if format == "csv" {
				rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
				require.NoError(t, err)
				require.Len(t, rows, 4)
				first, second, third := record(rows[0], rows[1]), record(rows[0], rows[2]), record(rows[0], rows[3])
				require.Equal(t, "0", first["average_cpu_utilization"])
				require.Equal(t, "0.5", first["peak_cpu_utilization"])
				require.Equal(t, "1.5", first["cpu_observed_seconds"])
				require.Equal(t, "1024.5", first["average_memory_bytes"])
				require.Equal(t, "16", first["cpu_capacity"])
				require.Equal(t, "32000000000", first["memory_capacity_bytes"])
				require.Equal(t, "2026-10-01T01:00:00.123Z", first["finished_at"])
				require.Empty(t, second["average_cpu_utilization"])
				require.Empty(t, second["cpu_capacity"])
				require.Equal(t, "0", second["cpu_sample_count"])
				require.Equal(t, "build-no-machine", third["build_id"])
				require.Empty(t, third["machine_id"])
			} else {
				for _, s := range []string{"0.00%", "50.00%", "n/a", "amd64", "arm64", "16", "32000000000", "1.50", "2.75", "1024.50", "build-no-machine", "No recorded machine association", "Observed executor work only"} {
					require.Contains(t, out, s)
				}
			}
		})
	}
}

func record(headers, values []string) map[string]string {
	r := make(map[string]string)
	for i, h := range headers {
		r[h] = values[i]
	}
	return r
}

func TestAnalyticsUsesServerAggregatesAndArchitectureBreakdown(t *testing.T) {
	want := &corev1.GetProjectBuildMetricsResponse{BuildCount: 42, BuildsWithoutMachines: 2, Statistics: measured(), Architectures: []*corev1.BuildArchitectureMetrics{{Architecture: "arm64", Statistics: &corev1.BuildResourceStatistics{}}}}
	var calls atomic.Int32
	serve(t, &metricsServer{analytics: func(_ context.Context, req *connect.Request[corev1.GetProjectBuildMetricsRequest]) (*connect.Response[corev1.GetProjectBuildMetricsResponse], error) {
		calls.Add(1)
		if req.Msg.ProjectId != "project-test" || req.Header().Get("Authorization") != "Bearer org-test" {
			t.Error("wrong tenant/project")
		}
		return connect.NewResponse(want), nil
	}})
	for _, format := range []string{"json", "csv", "table"} {
		out, stderr, err := execute(context.Background(), "analytics", "--output", format)
		require.NoError(t, err)
		require.Empty(t, stderr)
		switch format {
		case "json":
			var actual corev1.GetProjectBuildMetricsResponse
			require.NoError(t, protojson.Unmarshal([]byte(out), &actual))
			require.True(t, proto.Equal(want, &actual))
		case "csv":
			rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
			require.NoError(t, err)
			require.Len(t, rows, 3)
			all, arch := record(rows[0], rows[1]), record(rows[0], rows[2])
			require.Equal(t, "project", all["scope"])
			require.Equal(t, "42", all["build_count"])
			require.Equal(t, "2", all["builds_without_machines"])
			require.Equal(t, "3.75", all["cpu_time_seconds"])
			require.Equal(t, "architecture", arch["scope"])
			require.Equal(t, "arm64", arch["architecture"])
			require.Empty(t, arch["average_cpu_utilization"])
			require.Empty(t, arch["build_count"], "no invented per-architecture build count")
		case "table":
			for _, s := range []string{"Completed builds: 42", "Without recorded machines: 2", "duration-weighted", "0.00%", "50.00%", "arm64", "n/a", "not simultaneous"} {
				require.Contains(t, out, s)
			}
		}
	}
	require.EqualValues(t, 3, calls.Load())
}

func TestEmptyPageAndLocalProjectSelection(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("depot.json", []byte(`{"id":"local-project"}`), 0600))
	serve(t, &metricsServer{list: func(_ context.Context, req *connect.Request[corev1.ListBuildMetricsRequest]) (*connect.Response[corev1.ListBuildMetricsResponse], error) {
		if req.Msg.ProjectId != "local-project" {
			t.Errorf("wrong project: %s", req.Msg.ProjectId)
		}
		return connect.NewResponse(&corev1.ListBuildMetricsResponse{}), nil
	}})
	t.Setenv("DEPOT_PROJECT_ID", "")
	for _, format := range []string{"json", "csv", "table"} {
		out, stderr, err := execute(context.Background(), "metrics", "--output", format)
		require.NoError(t, err)
		require.Empty(t, stderr)
		switch format {
		case "json":
			require.JSONEq(t, `{"builds":[],"nextPageToken":""}`, out)
		case "csv":
			rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
			require.NoError(t, err)
			require.Len(t, rows, 1)
		case "table":
			require.Contains(t, out, "No completed builds")
		}
	}
}

func TestInvalidInputsNeverCallAPI(t *testing.T) {
	var calls atomic.Int32
	serve(t, &metricsServer{list: func(context.Context, *connect.Request[corev1.ListBuildMetricsRequest]) (*connect.Response[corev1.ListBuildMetricsResponse], error) {
		calls.Add(1)
		return connect.NewResponse(&corev1.ListBuildMetricsResponse{}), nil
	}})
	for _, args := range [][]string{
		{"--start", ""}, {"--end", ""}, {"--start", "yesterday"}, {"--start", "1969-12-31T00:00:00Z", "--end", "1970-01-01T00:00:00Z"}, {"--end", "2026-10-01T00:00:00Z"},
		{"--end", "2026-10-09T00:00:00Z"}, {"--start", "2099-10-01T00:00:00Z", "--end", "2099-10-02T00:00:00Z"},
		{"--start", "2026-10-01T00:00:00.000001Z"}, {"--output", "yaml"}, {"--page-size", "0"}, {"--page-size", "51"},
		{"--project", " "}, {"--token", " "}, {"unexpected"}, {"--org", "another-org"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, _, err := execute(context.Background(), "metrics", args...)
			require.Error(t, err)
			require.Empty(t, out)
		})
	}
	t.Setenv("DEPOT_TOKEN", "")
	out, _, err := execute(context.Background(), "metrics")
	require.ErrorContains(t, err, "organization API token")
	require.Empty(t, out)
	require.Zero(t, calls.Load())
}

func TestAPIErrorDoesNotPrintPartialResultsOrServerSecrets(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeNotFound, connect.CodeUnavailable, connect.CodeResourceExhausted, connect.CodeUnimplemented} {
		t.Run(code.String(), func(t *testing.T) {
			serve(t, &metricsServer{list: func(context.Context, *connect.Request[corev1.ListBuildMetricsRequest]) (*connect.Response[corev1.ListBuildMetricsResponse], error) {
				return nil, connect.NewError(code, errors.New("sensitive-server-message"))
			}})
			out, stderr, err := execute(context.Background(), "metrics", "--output", "csv")
			require.ErrorContains(t, err, code.String())
			require.NotContains(t, err.Error(), "sensitive-server-message")
			require.Empty(t, out)
			require.Empty(t, stderr)
			if code == connect.CodeResourceExhausted {
				require.ErrorContains(t, err, "narrow")
			}
		})
	}
}

func TestCancellationReachesAPI(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	serve(t, &metricsServer{list: func(ctx context.Context, _ *connect.Request[corev1.ListBuildMetricsRequest]) (*connect.Response[corev1.ListBuildMetricsResponse], error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, _, err := execute(ctx, "metrics", "--output", "json"); finished <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach server")
	}
	cancel()
	select {
	case err := <-finished:
		require.ErrorContains(t, err, "canceled")
	case <-time.After(5 * time.Second):
		t.Fatal("command did not cancel")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive cancellation")
	}
}

func TestTableCannotInjectTerminalControls(t *testing.T) {
	page := samplePage()
	page.Builds[0].Hardware = "unsafe\nrow\t\x1b[2J\u202e"
	serve(t, &metricsServer{list: func(context.Context, *connect.Request[corev1.ListBuildMetricsRequest]) (*connect.Response[corev1.ListBuildMetricsResponse], error) {
		return connect.NewResponse(page), nil
	}})
	out, _, err := execute(context.Background(), "metrics", "--output", "table")
	require.NoError(t, err)
	for _, s := range []string{"\x1b", "\u202e", "unsafe\nrow"} {
		require.NotContains(t, out, s)
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestOutputFailuresPropagate(t *testing.T) {
	serve(t, &metricsServer{list: func(context.Context, *connect.Request[corev1.ListBuildMetricsRequest]) (*connect.Response[corev1.ListBuildMetricsResponse], error) {
		return connect.NewResponse(samplePage()), nil
	}})
	for _, format := range []string{"table", "csv", "json"} {
		cmd := NewCmdBuilds()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs(append(append([]string{"metrics"}, rangeArgs...), "--output", format))
		cmd.SetOut(failedWriter{})
		cmd.SetErr(io.Discard)
		require.ErrorIs(t, cmd.Execute(), io.ErrClosedPipe)
	}
}

func TestAutoOutputAndSevenDayBoundary(t *testing.T) {
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	require.NoError(t, err)
	originalStdout := os.Stdout
	os.Stdout = stdout
	t.Cleanup(func() { os.Stdout = originalStdout; require.NoError(t, stdout.Close()) })
	serve(t, &metricsServer{list: func(_ context.Context, req *connect.Request[corev1.ListBuildMetricsRequest]) (*connect.Response[corev1.ListBuildMetricsResponse], error) {
		if req.Msg.StartAt.AsTime().Format(time.RFC3339Nano) != "2026-10-01T00:00:00.123Z" || req.Msg.EndAt.AsTime().Sub(req.Msg.StartAt.AsTime()) != 7*24*time.Hour {
			t.Error("time zones, milliseconds, and the seven-day boundary must be preserved")
		}
		return connect.NewResponse(samplePage()), nil
	}})
	out, stderr, err := execute(context.Background(), "metrics", "--start", "2026-10-01T01:00:00.123+01:00", "--end", "2026-10-08T00:00:00.123Z")
	require.NoError(t, err)
	require.Empty(t, stderr)
	var actual corev1.ListBuildMetricsResponse
	require.NoError(t, protojson.Unmarshal([]byte(out), &actual))
	require.True(t, proto.Equal(samplePage(), &actual))
}
