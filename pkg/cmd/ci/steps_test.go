package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/proto/depot/ci/v1/civ1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"
)

type stepsHandler struct {
	civ1connect.UnimplementedCIServiceHandler
	call func(*connect.Request[civ1.GetJobAttemptStepsRequest]) (*civ1.GetJobAttemptStepsResponse, error)
}

func (h stepsHandler) GetJobAttemptSteps(_ context.Context, req *connect.Request[civ1.GetJobAttemptStepsRequest]) (*connect.Response[civ1.GetJobAttemptStepsResponse], error) {
	response, err := h.call(req)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func stepsServer(t *testing.T, call func(*connect.Request[civ1.GetJobAttemptStepsRequest]) (*civ1.GetJobAttemptStepsResponse, error)) {
	t.Helper()
	_, handler := civ1connect.NewCIServiceHandler(stepsHandler{call: call})
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
}

func runSteps(args ...string) (string, error) {
	cmd := NewCmdSteps()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs(append(args, "--token", "test-token", "--org", "org-a"))
	err := cmd.Execute()
	return out.String(), err
}

func TestStepsJSONUsesAuthenticatedAttemptAndPreservesMissingAndZero(t *testing.T) {
	stepsServer(t, func(req *connect.Request[civ1.GetJobAttemptStepsRequest]) (*civ1.GetJobAttemptStepsResponse, error) {
		if req.Msg.AttemptId != "attempt-a" || req.Header().Get("Authorization") != "Bearer test-token" || req.Header().Get("X-Depot-Org") != "org-a" {
			t.Fatalf("incorrect request: %v, headers: %v", req.Msg, req.Header())
		}
		return &civ1.GetJobAttemptStepsResponse{AttemptId: "attempt-a", Available: true, Complete: true, Steps: []*civ1.JobAttemptStep{
			{StepKey: "key-1", StepId: "build", Number: 1, Name: "Build", Status: "completed", DurationMs: proto.Int64(0)},
			{StepKey: "key-2", Number: 2, Name: "Skipped", Status: "completed", Conclusion: "skipped"},
		}}, nil
	})
	out, err := runSteps("attempt-a", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatal(err)
	}
	steps := document["steps"].([]any)
	if steps[0].(map[string]any)["duration_ms"] != "0" {
		t.Fatalf("zero duration lost: %s", out)
	}
	if _, present := steps[1].(map[string]any)["duration_ms"]; present {
		t.Fatalf("missing duration fabricated: %s", out)
	}
}

func TestStepsTextShowsCoverageAndSanitizesWorkerNames(t *testing.T) {
	stepsServer(t, func(*connect.Request[civ1.GetJobAttemptStepsRequest]) (*civ1.GetJobAttemptStepsResponse, error) {
		return &civ1.GetJobAttemptStepsResponse{Available: true, Truncated: true, Steps: []*civ1.JobAttemptStep{
			{Number: 1, Name: "Build\x1b[31m\nInjected", DurationMs: proto.Int64(1234)},
			{Number: 2, Name: "Still running", Status: "in_progress"},
		}}, nil
	})
	out, err := runSteps("attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Progress snapshot", "Partial result", "1.234s", "Still running"} {
		if !strings.Contains(out, text) {
			t.Fatalf("missing %q: %s", text, out)
		}
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "\nInjected") {
		t.Fatalf("unsafe terminal output: %q", out)
	}
}

func TestStepsMissingHistoryIsNotZeroDuration(t *testing.T) {
	stepsServer(t, func(*connect.Request[civ1.GetJobAttemptStepsRequest]) (*civ1.GetJobAttemptStepsResponse, error) {
		return &civ1.GetJobAttemptStepsResponse{}, nil
	})
	out, err := runSteps("attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "No step timings were recorded") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestStepsRejectsInvalidInputBeforeRequest(t *testing.T) {
	stepsServer(t, func(*connect.Request[civ1.GetJobAttemptStepsRequest]) (*civ1.GetJobAttemptStepsResponse, error) {
		t.Fatal("unexpected network call")
		return nil, nil
	})
	for _, args := range [][]string{{}, {"one", "two"}, {" "}, {"attempt-a", "--output", "csv"}} {
		out, err := runSteps(args...)
		if err == nil || out != "" {
			t.Fatalf("args %v: output %q, error %v", args, out, err)
		}
	}
}

func TestStepsServerErrorsDoNotPrintPartialJSON(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnimplemented, connect.CodeUnavailable, connect.CodeNotFound, connect.CodeFailedPrecondition} {
		t.Run(code.String(), func(t *testing.T) {
			stepsServer(t, func(*connect.Request[civ1.GetJobAttemptStepsRequest]) (*civ1.GetJobAttemptStepsResponse, error) {
				return nil, connect.NewError(code, fmt.Errorf("steps unavailable"))
			})
			out, err := runSteps("attempt-a", "--output", "json")
			if err == nil || connect.CodeOf(err) != code || out != "" {
				t.Fatalf("output %q, error %v", out, err)
			}
		})
	}
}
