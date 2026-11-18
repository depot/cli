package jobs

import (
	"encoding/json"
	"testing"
	"time"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestJobOOMOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		oom  *corev1.GithubActionsJobOOM
		want string
	}{
		{"older server", nil, "unavailable"},
		{"no runner", &corev1.GithubActionsJobOOM{UnavailableReason: "missing_runner_identity"}, "unavailable (missing_runner_identity)"},
		{"none observed", &corev1.GithubActionsJobOOM{Detected: proto.Bool(false)}, "none observed (does not rule out OOM)"},
		{"detected", &corev1.GithubActionsJobOOM{
			Detected: proto.Bool(true), Truncated: true,
			Events: []*corev1.GithubActionsJobOOMEvent{{
				Timestamp:  timestamppb.New(time.Date(2026, 10, 20, 10, 0, 1, 250000000, time.UTC)),
				ProcessPid: 42, ProcessName: "compiler\nforged\trow\x1b[2J\u202e",
			}},
		}, "detected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := &corev1.GithubActionsJob{JobId: "123"}
			serve(t, &jobServer{
				get: func(*connect.Request[corev1.GetGithubActionsJobRequest]) (*corev1.GetGithubActionsJobResponse, error) {
					return &corev1.GetGithubActionsJobResponse{Job: job, Oom: tc.oom}, nil
				},
				metrics: func(*connect.Request[corev1.GetGithubActionsJobMetricsRequest]) (*corev1.GetGithubActionsJobMetricsResponse, error) {
					return &corev1.GetGithubActionsJobMetricsResponse{Job: job, Oom: tc.oom}, nil
				},
			})
			for _, args := range [][]string{{"123"}, {"metrics", "123"}} {
				for _, format := range []string{"json", "table"} {
					cmdArgs := append(append([]string{}, args...), "--repo", "acme/api", "--token", "test-token", "--output", format)
					out, err := execute(NewCmdJobs(), cmdArgs...)
					require.NoError(t, err)
					if format == "json" {
						var document map[string]json.RawMessage
						require.NoError(t, json.Unmarshal([]byte(out), &document))
						if tc.oom != nil {
							var actual corev1.GithubActionsJobOOM
							require.NoError(t, protojson.Unmarshal(document["oom"], &actual))
							require.True(t, proto.Equal(tc.oom, &actual))
						}
					} else {
						require.Contains(t, out, "OOM:")
						require.Contains(t, out, tc.want)
						if tc.oom.GetDetected() {
							require.Contains(t, out, "PID 42")
							require.Contains(t, out, "2026-10-20T10:00:01")
							require.Contains(t, out, "first 100")
						}
						for _, unsafe := range []string{"\nforged", "\x1b", "\u202e"} {
							require.NotContains(t, out, unsafe)
						}
					}
				}
			}
		})
	}
}
