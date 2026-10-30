package jobs

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestMetricsPreserveSamplesAndServerStatistics(t *testing.T) {
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	expected := &corev1.GetGithubActionsJobMetricsResponse{
		Job: &corev1.GithubActionsJob{
			JobId: "9007199254740993", RunId: "9007199254740994", Repository: "acme/api", Name: "test", WorkflowName: "CI",
			RunnerLabel: "depot-ubuntu-latest", RequestedCpuCount: 4, RequestedMemoryGib: 8,
			Status: corev1.GithubActionsJobStatus_GITHUB_ACTIONS_JOB_STATUS_RUNNING, StartedAt: timestamppb.New(start),
		},
		Availability: &corev1.GithubActionsMetricsAvailability{Code: corev1.GithubActionsMetricsAvailabilityCode_GITHUB_ACTIONS_METRICS_AVAILABILITY_CODE_PARTIAL, Reason: "partial_metrics"},
		Stats: &corev1.GithubActionsMetricsStats{
			SampleCount: 6000, CpuSampleCount: 6000, MemorySampleCount: 5999,
			AverageCpuUtilization: proto.Float64(.125), PeakCpuUtilization: proto.Float64(.9),
			AverageMemoryUtilization: proto.Float64(.25), PeakMemoryUtilization: proto.Float64(.8),
			ObservedStartedAt: timestamppb.New(start), ObservedFinishedAt: timestamppb.New(start.Add(5999 * time.Second)),
		},
		Cap: &corev1.GithubActionsMetricsCapMetadata{
			RawSampleCount: 6000, ReturnedSampleCount: 5000, MaxReturnedSampleCount: 5000,
			Downsampled: true, DownsampleStrategy: "evenly_spaced",
		},
		SnapshotAt: timestamppb.New(start.Add(6000 * time.Second)),
	}
	for i := 0; i < 5000; i++ {
		expected.Samples = append(expected.Samples, &corev1.GithubActionsMetricSample{
			Timestamp:      timestamppb.New(start.Add(time.Duration(i*5999/4999) * time.Second)),
			CpuUtilization: proto.Float64(0), MemoryUtilization: proto.Float64(.1),
		})
	}
	expected.Samples[0].MemoryUtilization = nil
	serve(t, &jobServer{metrics: func(r *connect.Request[corev1.GetGithubActionsJobMetricsRequest]) (*corev1.GetGithubActionsJobMetricsResponse, error) {
		checkHeaders(t, r.Header())
		if r.Msg.JobId != expected.Job.JobId || r.Msg.Repository != "acme/api" {
			t.Errorf("incorrect job identity: %v", r.Msg)
		}
		return expected, nil
	}})
	for _, format := range []string{"json", "table"} {
		out, err := execute(NewCmdJobs(), "metrics", expected.Job.JobId, "--repo", "acme/api", "--org", "org-1", "--token", "test-token", "--output", format)
		require.NoError(t, err)
		if format == "json" {
			var actual corev1.GetGithubActionsJobMetricsResponse
			require.NoError(t, protojson.Unmarshal([]byte(out), &actual))
			require.True(t, proto.Equal(expected, &actual))
			var document map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(out), &document))
			var samples []map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(document["samples"], &samples))
			require.JSONEq(t, `0`, string(samples[0]["cpuUtilization"]))
			require.NotContains(t, samples[0], "memoryUtilization", "missing telemetry must remain absent")
		} else {
			for _, want := range []string{"9007199254740993", "9007199254740994", "acme/api", "running", "depot-ubuntu-latest", "8 GiB", "partial_metrics", "12.5%", "90.0%", "25.0%", "80.0%", "6000 samples", "5999 samples", "5000 returned / 6000 raw (limit 5000)", "evenly_spaced", "omit intermediate peaks", "2026-10-15T13:40:00Z", "--output json"} {
				require.Contains(t, out, want)
			}
			require.NotContains(t, out, "1970-")
		}
	}
}

func TestMetricsAutoOutputWhenRedirected(t *testing.T) {
	// SetOut captures command output but does not change terminal detection.
	// Redirect process stdout so this test is independent of the test runner's TTY.
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	require.NoError(t, err)
	originalStdout := os.Stdout
	os.Stdout = stdout
	t.Cleanup(func() {
		os.Stdout = originalStdout
		require.NoError(t, stdout.Close())
	})

	expected := &corev1.GetGithubActionsJobMetricsResponse{Job: &corev1.GithubActionsJob{JobId: "123"}}
	serve(t, &jobServer{metrics: func(*connect.Request[corev1.GetGithubActionsJobMetricsRequest]) (*corev1.GetGithubActionsJobMetricsResponse, error) {
		return expected, nil
	}})
	out, err := execute(NewCmdJobs(), "metrics", "123", "--repo", "acme/api", "--token", "test-token")
	require.NoError(t, err)
	var actual corev1.GetGithubActionsJobMetricsResponse
	require.NoError(t, protojson.Unmarshal([]byte(out), &actual))
	require.True(t, proto.Equal(expected, &actual))
}

func TestMetricsMissingTelemetryDoesNotBecomeZero(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "partial"}[partial], func(t *testing.T) {
			resp := &corev1.GetGithubActionsJobMetricsResponse{
				Job:          &corev1.GithubActionsJob{JobId: "123", Name: "test\nforged\trow\x1b[2J\u202e"},
				Availability: &corev1.GithubActionsMetricsAvailability{Code: corev1.GithubActionsMetricsAvailabilityCode_GITHUB_ACTIONS_METRICS_AVAILABILITY_CODE_UNAVAILABLE, Reason: "no_samples\x1b[2J"},
				Cap:          &corev1.GithubActionsMetricsCapMetadata{MaxReturnedSampleCount: 5000},
			}
			if partial {
				resp.Availability.Code = corev1.GithubActionsMetricsAvailabilityCode_GITHUB_ACTIONS_METRICS_AVAILABILITY_CODE_PARTIAL
				resp.Availability.Reason = "partial_metrics"
				resp.Stats = &corev1.GithubActionsMetricsStats{SampleCount: 1, CpuSampleCount: 1, AverageCpuUtilization: proto.Float64(0), PeakCpuUtilization: proto.Float64(0)}
				resp.Samples = []*corev1.GithubActionsMetricSample{{CpuUtilization: proto.Float64(0)}}
				resp.Cap.RawSampleCount, resp.Cap.ReturnedSampleCount = 1, 1
			}
			serve(t, &jobServer{metrics: func(*connect.Request[corev1.GetGithubActionsJobMetricsRequest]) (*corev1.GetGithubActionsJobMetricsResponse, error) {
				return resp, nil
			}})
			for _, format := range []string{"json", "table"} {
				out, err := execute(NewCmdJobs(), "metrics", "123", "--repo", "acme/api", "--token", "test-token", "--output", format)
				require.NoError(t, err)
				if format == "json" {
					var actual corev1.GetGithubActionsJobMetricsResponse
					require.NoError(t, protojson.Unmarshal([]byte(out), &actual))
					require.True(t, proto.Equal(resp, &actual))
					if !partial {
						require.Contains(t, strings.ReplaceAll(out, " ", ""), `"samples":[]`)
					}
				} else {
					require.Contains(t, out, "avg n/a, peak n/a (0 samples)")
					if partial {
						require.Contains(t, out, "avg 0.0%, peak 0.0% (1 samples)")
					} else {
						require.NotContains(t, out, "0.0%")
					}
					for _, unsafe := range []string{"\x1b", "\u202e", "\nforged", "1970-"} {
						require.NotContains(t, out, unsafe)
					}
				}
			}
		})
	}
}

func TestMetricsRejectInvalidInputBeforeAPI(t *testing.T) {
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected API request for invalid input: %s", r.URL.Path)
		w.WriteHeader(500)
	}), &http2.Server{}))
	defer server.Close()
	t.Setenv("DEPOT_API_URL", server.URL)
	for _, args := range [][]string{
		{}, {"123"}, {"123", "456"}, {" ", "--repo", "acme/api"},
		{"123", "--repo", ""}, {"123", "--repo", " "}, {"123", "--repo", "acme/api", "--repo", "acme/web"},
		{"123", "--repo", "acme/api", "--output", "yaml"}, {"123", "--repo", "acme/api", "--start", "2026-10-15T00:00:00Z"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := execute(NewCmdJobs(), append(append([]string{"metrics"}, args...), "--token", "test-token")...)
			require.Error(t, err)
			require.Empty(t, out)
		})
	}
}

func TestMetricsAPIErrorsProduceNoSuccessOutput(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeNotFound, connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeUnimplemented, connect.CodeUnavailable, connect.CodeResourceExhausted} {
		t.Run(code.String(), func(t *testing.T) {
			serve(t, &jobServer{metrics: func(*connect.Request[corev1.GetGithubActionsJobMetricsRequest]) (*corev1.GetGithubActionsJobMetricsResponse, error) {
				return nil, connect.NewError(code, errors.New("metrics request rejected"))
			}})
			for _, format := range []string{"json", "table"} {
				out, err := execute(NewCmdJobs(), "metrics", "123", "--repo", "acme/api", "--token", "test-token", "--output", format)
				require.ErrorContains(t, err, "failed to get GitHub Actions job metrics")
				require.Equal(t, code, connect.CodeOf(err))
				require.Empty(t, out)
			}
		})
	}
}

func TestMetricsMissingJobIsAnError(t *testing.T) {
	serve(t, &jobServer{metrics: func(*connect.Request[corev1.GetGithubActionsJobMetricsRequest]) (*corev1.GetGithubActionsJobMetricsResponse, error) {
		return &corev1.GetGithubActionsJobMetricsResponse{}, nil
	}})
	for _, format := range []string{"json", "table"} {
		out, err := execute(NewCmdJobs(), "metrics", "123", "--repo", "acme/api", "--token", "test-token", "--output", format)
		require.ErrorContains(t, err, "contained no job")
		require.Empty(t, out)
	}
}

func TestMetricsOutputWriteFailure(t *testing.T) {
	failure := errors.New("output closed")
	serve(t, &jobServer{metrics: func(*connect.Request[corev1.GetGithubActionsJobMetricsRequest]) (*corev1.GetGithubActionsJobMetricsResponse, error) {
		return &corev1.GetGithubActionsJobMetricsResponse{Job: &corev1.GithubActionsJob{JobId: "123"}}, nil
	}})
	for _, format := range []string{"json", "table"} {
		cmd := NewCmdJobs()
		cmd.SetOut(failingWriter{failure})
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs([]string{"metrics", "123", "--repo", "acme/api", "--token", "test-token", "--output", format})
		require.ErrorIs(t, cmd.Execute(), failure)
	}
}
