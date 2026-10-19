package jobs

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type jobServer struct {
	corev1connect.UnimplementedGithubActionsServiceHandler
	list      func(*connect.Request[corev1.ListGithubActionsJobsRequest]) (*corev1.ListGithubActionsJobsResponse, error)
	get       func(*connect.Request[corev1.GetGithubActionsJobRequest]) (*corev1.GetGithubActionsJobResponse, error)
	analytics func(*connect.Request[corev1.ListGithubActionsJobAnalyticsRequest]) (*corev1.ListGithubActionsJobAnalyticsResponse, error)
}

func (s *jobServer) ListGithubActionsJobs(_ context.Context, r *connect.Request[corev1.ListGithubActionsJobsRequest]) (*connect.Response[corev1.ListGithubActionsJobsResponse], error) {
	v, e := s.list(r)
	if e != nil {
		return nil, e
	}
	return connect.NewResponse(v), nil
}
func (s *jobServer) GetGithubActionsJob(_ context.Context, r *connect.Request[corev1.GetGithubActionsJobRequest]) (*connect.Response[corev1.GetGithubActionsJobResponse], error) {
	v, e := s.get(r)
	if e != nil {
		return nil, e
	}
	return connect.NewResponse(v), nil
}
func (s *jobServer) ListGithubActionsJobAnalytics(_ context.Context, r *connect.Request[corev1.ListGithubActionsJobAnalyticsRequest]) (*connect.Response[corev1.ListGithubActionsJobAnalyticsResponse], error) {
	v, e := s.analytics(r)
	if e != nil {
		return nil, e
	}
	return connect.NewResponse(v), nil
}
func serve(t *testing.T, s *jobServer) {
	t.Helper()
	path, h := corev1connect.NewGithubActionsServiceHandler(s)
	mux := http.NewServeMux()
	mux.Handle(path, h)
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
}
func execute(cmd *cobra.Command, args ...string) (string, error) {
	var b bytes.Buffer
	cmd.SetOut(&b)
	cmd.SetErr(&b)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return b.String(), err
}
func checkHeaders(t *testing.T, h http.Header) {
	t.Helper()
	// Error rather than Fatal: the handler runs on a server goroutine.
	if h.Get("Authorization") != "Bearer test-token" {
		t.Errorf("missing explicit API credential")
	}
	if h.Get("x-depot-org") != "org-1" {
		t.Errorf("missing organization scope")
	}
}
func TestListJSONRoundTrip(t *testing.T) {
	for _, factory := range []func() *cobra.Command{NewCmdJobs, NewCmdListJobs} {
		t.Run(factory().Use, func(t *testing.T) {
			var got *corev1.ListGithubActionsJobsRequest
			expected := &corev1.ListGithubActionsJobsResponse{
				Jobs:      []*corev1.GithubActionsJob{{JobId: "9007199254740993", Name: "build\nunit", DurationSeconds: 123, QueueSeconds: 4}},
				TotalSize: 9007199254740993, NextPageToken: proto.String("next+page=="), DataThrough: timestamppb.New(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)),
			}
			serve(t, &jobServer{list: func(r *connect.Request[corev1.ListGithubActionsJobsRequest]) (*corev1.ListGithubActionsJobsResponse, error) {
				checkHeaders(t, r.Header())
				got = r.Msg
				return expected, nil
			}})
			output, err := execute(factory(), "--org", "org-1", "--token", "test-token", "--output", "json", "--repo", "acme/api", "--repo", "acme/web", "--status", "finished", "--conclusion", "failure", "--runner-label", "depot-ubuntu-latest", "--query", "test", "--page-size", "7", "--page-token", "previous", "--start", "2026-10-01T01:00:00+01:00", "--end", "2026-10-08T00:00:00Z")
			require.NoError(t, err)
			var actual corev1.ListGithubActionsJobsResponse
			require.NoError(t, protojson.Unmarshal([]byte(output), &actual))
			require.True(t, proto.Equal(expected, &actual), output)
			require.Equal(t, []string{"acme/api", "acme/web"}, got.Repositories)
			require.Equal(t, []corev1.GithubActionsJobStatus{corev1.GithubActionsJobStatus_GITHUB_ACTIONS_JOB_STATUS_FINISHED}, got.Statuses)
			require.Equal(t, []corev1.GithubActionsJobConclusion{corev1.GithubActionsJobConclusion_GITHUB_ACTIONS_JOB_CONCLUSION_FAILURE}, got.Conclusions)
			require.Equal(t, []string{"depot-ubuntu-latest"}, got.RunnerLabels)
			require.Equal(t, "test", got.GetQuery())
			require.Equal(t, int32(7), got.GetPageSize())
			require.Equal(t, "previous", got.GetPageToken())
			require.Equal(t, "2026-10-01T00:00:00Z", got.TimeRange.StartAt.AsTime().Format(time.RFC3339))
			require.Equal(t, "2026-10-08T00:00:00Z", got.TimeRange.EndAt.AsTime().Format(time.RFC3339))
		})
	}
}
func TestGetJob(t *testing.T) {
	job := &corev1.GithubActionsJob{JobId: "123", Repository: "acme/api", Name: "test", Status: corev1.GithubActionsJobStatus_GITHUB_ACTIONS_JOB_STATUS_FINISHED, Conclusion: corev1.GithubActionsJobConclusion_GITHUB_ACTIONS_JOB_CONCLUSION_SUCCESS, DurationSeconds: 123, QueueSeconds: 4, BillableSeconds: 125, RequestedCpuCount: 4, RequestedMemoryGib: 8}
	serve(t, &jobServer{get: func(r *connect.Request[corev1.GetGithubActionsJobRequest]) (*corev1.GetGithubActionsJobResponse, error) {
		checkHeaders(t, r.Header())
		if r.Msg.JobId != "123" || r.Msg.Repository != "acme/api" {
			t.Errorf("incorrect job identity: %v", r.Msg)
		}
		return &corev1.GetGithubActionsJobResponse{Job: job}, nil
	}})
	for _, format := range []string{"json", "table"} {
		output, err := execute(NewCmdJobs(), "123", "--repo", "acme/api", "--token", "test-token", "--org", "org-1", "--output", format)
		require.NoError(t, err)
		if format == "json" {
			var resp corev1.GetGithubActionsJobResponse
			require.NoError(t, protojson.Unmarshal([]byte(output), &resp))
			require.True(t, proto.Equal(job, resp.Job))
		} else {
			for _, want := range []string{"Job ID:", "123", "success", "123s", "4s", "125s", "8 GiB", "Finished:"} {
				require.Contains(t, output, want)
			}
			require.NotContains(t, output, "1970-")
		}
	}
}
func TestAnalytics(t *testing.T) {
	var got *corev1.ListGithubActionsJobAnalyticsRequest
	expected := &corev1.ListGithubActionsJobAnalyticsResponse{Jobs: []*corev1.GithubActionsJobAnalytics{{Repository: "acme/api", WorkflowName: "CI", WorkflowPath: ".github/workflows/ci.yml", JobName: "test", RunnerLabel: "depot-ubuntu-latest", JobCount: 9007199254740993, AverageCpuUtilization: .125, PeakCpuUtilization: .8, AverageMemoryUtilization: .25, PeakMemoryUtilization: .9, AverageDurationSeconds: 61.5, P95DurationSeconds: 123.4, FailureRate: .01}}, NextPageToken: proto.String("next-page"), DataThrough: timestamppb.New(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC))}
	serve(t, &jobServer{analytics: func(r *connect.Request[corev1.ListGithubActionsJobAnalyticsRequest]) (*corev1.ListGithubActionsJobAnalyticsResponse, error) {
		checkHeaders(t, r.Header())
		got = r.Msg
		return expected, nil
	}})
	for _, format := range []string{"json", "table"} {
		output, err := execute(NewCmdJobs(), "analytics", "--repo", "acme/api", "--workflow", "CI", "--workflow-path", ".github/workflows/ci.yml", "--job", "test", "--runner-label", "depot-ubuntu-latest", "--page-size", "5", "--page-token", "previous", "--start", "2026-10-01T00:00:00Z", "--end", "2026-10-08T00:00:00Z", "--token", "test-token", "--org", "org-1", "--output", format)
		require.NoError(t, err)
		require.Equal(t, []string{"acme/api"}, got.Filters.Repositories)
		require.Equal(t, []string{"CI"}, got.Filters.Workflows)
		require.Equal(t, []string{".github/workflows/ci.yml"}, got.Filters.WorkflowPaths)
		require.Equal(t, []string{"test"}, got.Filters.Jobs)
		require.Equal(t, []string{"depot-ubuntu-latest"}, got.Filters.RunnerLabels)
		require.Equal(t, int32(5), got.GetPageSize())
		require.Equal(t, "previous", got.GetPageToken())
		require.Equal(t, 7*24*time.Hour, got.TimeRange.EndAt.AsTime().Sub(got.TimeRange.StartAt.AsTime()))
		if format == "json" {
			var resp corev1.ListGithubActionsJobAnalyticsResponse
			require.NoError(t, protojson.Unmarshal([]byte(output), &resp))
			require.True(t, proto.Equal(expected, &resp), output)
		} else {
			for _, want := range []string{"12.5%", "80.0%", "25.0%", "90.0%", "61.5s", "123.4s", "1.0%", "9007199254740993", ".github/workflows/ci.yml", "Next page token: next-page", "Data through: 2026-10-08T00:00:00Z"} {
				require.Contains(t, output, want)
			}
		}
	}
}
func TestRejectInvalidOptionsBeforeAPI(t *testing.T) {
	cases := [][]string{
		{"--page-size", "0"}, {"--page-size", "251"}, {"--page-size", "-1"}, {"--output", "yaml"}, {"--status", "success"}, {"--conclusion", "running"},
		{"--start", "2026-10-01T00:00:00Z"}, {"--end", "2026-10-01T00:00:00Z"},
		{"--start", "bad", "--end", "2026-10-01T00:00:00Z"},
		{"--start", "2026-10-01T00:00:00Z", "--end", "bad"},
		{"--start", "2026-10-01T00:00:00Z", "--end", "2026-10-01T00:00:00Z"},
		{"--start", "2026-01-01T00:00:00Z", "--end", "2026-10-01T00:00:00Z"},
		{"--start", "0000-01-01T00:00:00Z", "--end", "0000-01-02T00:00:00Z"},
		{"123"}, {"123", "--repo", ""}, {"123", "--repo", "acme/api", "--repo", "acme/web"}, {"123", "--repo", "acme/api", "--page-size", "50"},
		{"123", "456"}, {"analytics", "extra"}, {"analytics", "--page-size", "251"}, {"analytics", "--output", "yaml"},
	}
	// A reachable server makes an accidental request visible instead of accepting
	// a connection error as evidence of local validation.
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected API request for invalid input: %s", r.URL.Path)
		w.WriteHeader(500)
	}), &http2.Server{}))
	defer server.Close()
	t.Setenv("DEPOT_API_URL", server.URL)
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			output, err := execute(NewCmdJobs(), append(args, "--token", "test-token")...)
			require.Error(t, err)
			require.Empty(t, output)
		})
	}
}
func TestAPIErrorsAreReturnedWithoutSuccessOutput(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeUnimplemented, connect.CodeUnavailable} {
		t.Run(code.String(), func(t *testing.T) {
			apiErr := connect.NewError(code, errors.New("request rejected"))
			serve(t, &jobServer{
				list: func(*connect.Request[corev1.ListGithubActionsJobsRequest]) (*corev1.ListGithubActionsJobsResponse, error) {
					return nil, apiErr
				},
				get: func(*connect.Request[corev1.GetGithubActionsJobRequest]) (*corev1.GetGithubActionsJobResponse, error) {
					return nil, apiErr
				},
				analytics: func(*connect.Request[corev1.ListGithubActionsJobAnalyticsRequest]) (*corev1.ListGithubActionsJobAnalyticsResponse, error) {
					return nil, apiErr
				},
			})
			for _, args := range [][]string{nil, {"123", "--repo", "acme/api"}, {"analytics"}} {
				output, err := execute(NewCmdJobs(), append(args, "--token", "test-token", "--output", "json")...)
				require.Error(t, err)
				require.Equal(t, code, connect.CodeOf(err))
				require.Empty(t, output)
			}
		})
	}
}
func TestEmptyPagesAndDefaultWindow(t *testing.T) {
	serve(t, &jobServer{
		list: func(r *connect.Request[corev1.ListGithubActionsJobsRequest]) (*corev1.ListGithubActionsJobsResponse, error) {
			if r.Msg.TimeRange != nil {
				t.Error("default window must be resolved by API")
			}
			return &corev1.ListGithubActionsJobsResponse{}, nil
		},
		analytics: func(r *connect.Request[corev1.ListGithubActionsJobAnalyticsRequest]) (*corev1.ListGithubActionsJobAnalyticsResponse, error) {
			if r.Msg.TimeRange != nil {
				t.Error("default window must be resolved by API")
			}
			return &corev1.ListGithubActionsJobAnalyticsResponse{}, nil
		},
	})
	for _, args := range [][]string{nil, {"analytics"}} {
		for _, format := range []string{"json", "table"} {
			out, err := execute(NewCmdJobs(), append(args, "--token", "test-token", "--output", format)...)
			require.NoError(t, err)
			if format == "json" {
				require.Contains(t, strings.ReplaceAll(out, " ", ""), `"jobs":[]`)
			} else {
				require.Contains(t, out, "No matching GitHub Actions")
			}
		}
	}
}
func TestTableSanitizesUntrustedText(t *testing.T) {
	serve(t, &jobServer{list: func(*connect.Request[corev1.ListGithubActionsJobsRequest]) (*corev1.ListGithubActionsJobsResponse, error) {
		return &corev1.ListGithubActionsJobsResponse{Jobs: []*corev1.GithubActionsJob{{Name: "test\nforged\trow\x1b[2J\u009b31m\u202eevil", JobId: "123", DurationSeconds: 7}}, NextPageToken: proto.String("next\x1b[2J")}, nil
	}})
	out, err := execute(NewCmdJobs(), "--token", "test-token", "--output", "table")
	require.NoError(t, err)
	for _, unsafe := range []string{"\x1b", "\u009b", "\u202e", "\nforged"} {
		require.NotContains(t, out, unsafe)
	}
	require.Contains(t, out, "test forged row")
	require.Contains(t, out, "7s")
}
func TestEnvironmentToken(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "test-token")
	serve(t, &jobServer{list: func(r *connect.Request[corev1.ListGithubActionsJobsRequest]) (*corev1.ListGithubActionsJobsResponse, error) {
		checkHeaders(t, r.Header())
		return &corev1.ListGithubActionsJobsResponse{}, nil
	}})
	out, err := execute(NewCmdJobs(), "--org", "org-1", "--output", "json")
	require.NoError(t, err)
	require.Contains(t, out, "jobs")
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestOutputWriteFailuresAreReturned(t *testing.T) {
	failure := errors.New("output closed")
	for _, render := range []func() error{
		func() error { return writeJobs(failingWriter{failure}, &corev1.ListGithubActionsJobsResponse{}) },
		func() error {
			return writeAnalytics(failingWriter{failure}, &corev1.ListGithubActionsJobAnalyticsResponse{})
		},
		func() error { return writeJob(failingWriter{failure}, &corev1.GithubActionsJob{Name: "test"}) },
		func() error { return writeJSON(failingWriter{failure}, &corev1.ListGithubActionsJobsResponse{}) },
	} {
		require.ErrorIs(t, render(), failure)
	}
}

func TestMissingJobIsAnErrorInEveryFormat(t *testing.T) {
	serve(t, &jobServer{get: func(*connect.Request[corev1.GetGithubActionsJobRequest]) (*corev1.GetGithubActionsJobResponse, error) {
		return &corev1.GetGithubActionsJobResponse{}, nil
	}})
	for _, format := range []string{"table", "json"} {
		out, err := execute(NewCmdJobs(), "123", "--repo", "acme/api", "--token", "test-token", "--output", format)
		require.ErrorContains(t, err, "contained no job")
		require.Empty(t, out)
	}
}
