package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func TestBuildCancelRoutingPreservesBuildContexts(t *testing.T) {
	for _, args := range [][]string{{"."}, {"./cancel"}, {"--", "cancel"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			command := NewCmdBuild()
			var contexts []string
			command.RunE = func(_ *cobra.Command, args []string) error {
				contexts = args
				return nil
			}
			_, _, err := executeCancelCommand(command, context.Background(), args...)
			if err != nil {
				t.Fatal(err)
			}
			if len(contexts) != 1 || contexts[0] != args[len(args)-1] {
				t.Fatalf("build contexts = %v, want %q", contexts, args[len(args)-1])
			}
		})
	}

	handler := &cancelBuildService{}
	setupCancelBuildService(t, handler)
	stdout, _, err := executeCancelCommand(NewCmdBuild(), context.Background(), "cancel", "build-a", "--token", "token", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	output := decodeCancellationOutput(t, stdout)
	assertCancellationResults(t, output, map[string]string{"build-a": "OUTCOME_CANCELLATION_REQUESTED"})
}

func TestCancelBuildsReportsEachOutcomeAndDeduplicatesIDs(t *testing.T) {
	handler := &cancelBuildService{cancel: func(_ context.Context, request *connect.Request[corev1.CancelBuildRequest]) (*connect.Response[corev1.CancelBuildResponse], error) {
		outcome := corev1.CancelBuildResponse_OUTCOME_CANCELLATION_REQUESTED
		if request.Msg.GetBuildId() == "finished" {
			outcome = corev1.CancelBuildResponse_OUTCOME_ALREADY_TERMINAL
		}
		return connect.NewResponse(&corev1.CancelBuildResponse{Outcome: outcome}), nil
	}}
	setupCancelBuildService(t, handler)

	stdout, _, err := executeCancelCommand(NewCmdCancel(), context.Background(), "running", "finished", "running", "--token", "token", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	output := decodeCancellationOutput(t, stdout)
	if output.DryRun == nil || *output.DryRun {
		t.Fatalf("dry_run = %v, want false", output.DryRun)
	}
	assertCancellationResults(t, output, map[string]string{"running": "OUTCOME_CANCELLATION_REQUESTED", "finished": "OUTCOME_ALREADY_TERMINAL"})
	assertCancelledBuilds(t, handler, "running", "finished")
}

func TestCancelBuildsContinuesAfterPerBuildErrors(t *testing.T) {
	codes := map[string]connect.Code{
		"missing": connect.CodeNotFound,
		"denied":  connect.CodePermissionDenied,
	}
	handler := &cancelBuildService{cancel: func(_ context.Context, request *connect.Request[corev1.CancelBuildRequest]) (*connect.Response[corev1.CancelBuildResponse], error) {
		if code, ok := codes[request.Msg.GetBuildId()]; ok {
			return nil, connect.NewError(code, errors.New("request rejected"))
		}
		return connect.NewResponse(&corev1.CancelBuildResponse{Outcome: corev1.CancelBuildResponse_OUTCOME_CANCELLATION_REQUESTED}), nil
	}}
	setupCancelBuildService(t, handler)

	stdout, _, err := executeCancelCommand(NewCmdCancel(), context.Background(), "missing", "running", "denied", "--token", "token", "-o", "json")
	if err == nil {
		t.Fatal("partial failure returned success")
	}
	output := decodeCancellationOutput(t, stdout)
	if len(output.Results) != 3 {
		t.Fatalf("results = %#v, want all three builds", output.Results)
	}
	for _, result := range output.Results {
		if code, ok := codes[result.BuildID]; ok {
			if result.Error == nil || result.Error.Code != code.String() || result.Error.Message == "" || result.Outcome != "" {
				t.Errorf("failure for %q = %#v", result.BuildID, result)
			}
		} else if result.BuildID != "running" || result.Outcome != "OUTCOME_CANCELLATION_REQUESTED" || result.Error != nil {
			t.Errorf("successful result = %#v", result)
		}
	}
	assertCancelledBuilds(t, handler, "missing", "running", "denied")
}

func TestCancelBuildsRejectsUnsupportedOrUnknownServerResponses(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *corev1.CancelBuildResponse
		err      error
	}{
		{name: "unsupported server", err: connect.NewError(connect.CodeUnimplemented, errors.New("method unavailable"))},
		{name: "unspecified outcome", response: &corev1.CancelBuildResponse{}},
		{name: "unknown outcome", response: &corev1.CancelBuildResponse{Outcome: corev1.CancelBuildResponse_Outcome(999)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := &cancelBuildService{cancel: func(context.Context, *connect.Request[corev1.CancelBuildRequest]) (*connect.Response[corev1.CancelBuildResponse], error) {
				if test.err != nil {
					return nil, test.err
				}
				return connect.NewResponse(test.response), nil
			}}
			setupCancelBuildService(t, handler)
			stdout, _, err := executeCancelCommand(NewCmdCancel(), context.Background(), "build-a", "--token", "token", "-o", "json")
			if err == nil {
				t.Fatal("unconfirmed cancellation returned success")
			}
			output := decodeCancellationOutput(t, stdout)
			if len(output.Results) != 1 || output.Results[0].BuildID != "build-a" || output.Results[0].Outcome != "" || output.Results[0].Error == nil {
				t.Fatalf("results = %#v, want a per-build error with no success outcome", output.Results)
			}
		})
	}
}

func TestCancelRunningBuildsCollectsAllPagesBeforeMutating(t *testing.T) {
	var pages atomic.Int32
	handler := &cancelBuildService{
		list: func(_ context.Context, request *connect.Request[corev1.ListBuildsRequest]) (*connect.Response[corev1.ListBuildsResponse], error) {
			pages.Add(1)
			if request.Msg.GetProjectId() != "project-a" || request.Header().Get("Authorization") != "Bearer token" {
				t.Errorf("list project = %q, authorization = %q", request.Msg.GetProjectId(), request.Header().Get("Authorization"))
			}
			switch request.Msg.GetPageToken() {
			case "":
				return connect.NewResponse(&corev1.ListBuildsResponse{
					Builds: []*corev1.Build{
						{BuildId: "running-a", Status: corev1.Build_STATUS_RUNNING},
						{BuildId: "finished", Status: corev1.Build_STATUS_SUCCESS},
						{BuildId: "unknown", Status: corev1.Build_STATUS_UNSPECIFIED},
					},
					NextPageToken: "next",
				}), nil
			case "next":
				return connect.NewResponse(&corev1.ListBuildsResponse{Builds: []*corev1.Build{
					{BuildId: "running-a", Status: corev1.Build_STATUS_RUNNING},
					{BuildId: "running-b", Status: corev1.Build_STATUS_RUNNING},
				}}), nil
			default:
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unexpected cursor"))
			}
		},
		cancel: func(_ context.Context, request *connect.Request[corev1.CancelBuildRequest]) (*connect.Response[corev1.CancelBuildResponse], error) {
			if pages.Load() != 2 {
				t.Error("cancellation started before build selection completed")
			}
			outcome := corev1.CancelBuildResponse_OUTCOME_CANCELLATION_REQUESTED
			if request.Msg.GetBuildId() == "running-b" {
				outcome = corev1.CancelBuildResponse_OUTCOME_ALREADY_TERMINAL
			}
			return connect.NewResponse(&corev1.CancelBuildResponse{Outcome: outcome}), nil
		},
	}
	setupCancelBuildService(t, handler)

	stdout, _, err := executeCancelCommand(NewCmdCancel(), context.Background(), "--all-running", "--project", "project-a", "-y", "--token", "token", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	assertCancellationResults(t, decodeCancellationOutput(t, stdout), map[string]string{"running-a": "OUTCOME_CANCELLATION_REQUESTED", "running-b": "OUTCOME_ALREADY_TERMINAL"})
	assertCancelledBuilds(t, handler, "running-a", "running-b")
}

func TestCancelRunningBuildsNeverMutatesIncompleteSelection(t *testing.T) {
	for _, mode := range []string{"page failure", "repeated cursor"} {
		t.Run(mode, func(t *testing.T) {
			var pages atomic.Int32
			handler := &cancelBuildService{list: func(_ context.Context, request *connect.Request[corev1.ListBuildsRequest]) (*connect.Response[corev1.ListBuildsResponse], error) {
				if pages.Add(1) > 3 {
					return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("pagination did not terminate"))
				}
				if mode == "page failure" && request.Msg.GetPageToken() != "" {
					return nil, connect.NewError(connect.CodeUnavailable, errors.New("listing failed"))
				}
				return connect.NewResponse(&corev1.ListBuildsResponse{
					Builds:        []*corev1.Build{{BuildId: "running", Status: corev1.Build_STATUS_RUNNING}},
					NextPageToken: "same-cursor",
				}), nil
			}}
			setupCancelBuildService(t, handler)
			_, _, err := executeCancelCommand(NewCmdCancel(), context.Background(), "--all-running", "--project", "project-a", "--yes", "--token", "token")
			if err == nil {
				t.Fatal("incomplete selection returned success")
			}
			if pages.Load() != 2 {
				t.Fatalf("list requests = %d, want 2", pages.Load())
			}
			assertCancelledBuilds(t, handler)
		})
	}
}

func TestCancelBuildsDryRunAndBulkConfirmation(t *testing.T) {
	for _, test := range []struct {
		name       string
		args       []string
		empty      bool
		wantDryRun bool
		wantError  bool
	}{
		{name: "explicit preview", args: []string{"build-a", "--dry-run"}, wantDryRun: true},
		{name: "bulk preview", args: []string{"--all-running", "--project", "project-a", "--dry-run"}, wantDryRun: true},
		{name: "bulk requires confirmation", args: []string{"--all-running", "--project", "project-a"}, wantError: true},
		{name: "empty bulk is a no-op", args: []string{"--all-running", "--project", "project-a"}, empty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := &cancelBuildService{list: func(context.Context, *connect.Request[corev1.ListBuildsRequest]) (*connect.Response[corev1.ListBuildsResponse], error) {
				response := &corev1.ListBuildsResponse{}
				if !test.empty {
					response.Builds = []*corev1.Build{{BuildId: "build-a", Status: corev1.Build_STATUS_RUNNING}}
				}
				return connect.NewResponse(response), nil
			}}
			setupCancelBuildService(t, handler)
			stdout, _, err := executeCancelCommand(NewCmdCancel(), context.Background(), append(test.args, "--token", "token", "-o", "json")...)
			assertCancelledBuilds(t, handler)
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "--yes") {
					t.Fatalf("error = %v, want instructions to use --yes", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			output := decodeCancellationOutput(t, stdout)
			if output.DryRun == nil || *output.DryRun != test.wantDryRun {
				t.Fatalf("dry_run = %v, want %v", output.DryRun, test.wantDryRun)
			}
			if test.empty {
				if output.Results == nil || len(output.Results) != 0 {
					t.Fatalf("results = %#v, want []", output.Results)
				}
			} else {
				assertCancellationResults(t, output, map[string]string{"build-a": "WOULD_REQUEST_CANCELLATION"})
			}
		})
	}
}

func TestCancelBuildsInvalidArgumentsDoNotContactAPI(t *testing.T) {
	t.Setenv("DEPOT_PROJECT_ID", "implicit-project")
	for _, args := range [][]string{
		{},
		{"--all-running"},
		{"--project", "project-a"},
		{"build-a", "--all-running", "--project", "project-a"},
		{"build-a", "--project", "project-a"},
		{"--all-running", "--project", ""},
		{"build-a", "--output", "yaml"},
		{"build-a", "--org", "org-a"},
	} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			handler := &cancelBuildService{}
			setupCancelBuildService(t, handler)
			_, _, err := executeCancelCommand(NewCmdCancel(), context.Background(), append(args, "--token", "token")...)
			if err == nil {
				t.Fatal("invalid arguments returned success")
			}
			if count := handler.requests.Load(); count != 0 {
				t.Fatalf("invalid arguments sent %d HTTP requests", count)
			}
		})
	}
}

func TestCancelBuildsUsesStaticCredentialsWithoutLogin(t *testing.T) {
	previous := viper.GetString("api_token")
	t.Cleanup(func() { viper.Set("api_token", previous) })
	for _, key := range []string{"DEPOT_API_TOKEN", "DEPOT_TOKEN", "DEPOT_JIT_TOKEN", "DEPOT_CACHE_TOKEN"} {
		t.Setenv(key, "")
	}
	for _, source := range []string{"flag", "environment", "stored", "missing"} {
		t.Run(source, func(t *testing.T) {
			viper.Set("api_token", "")
			const token = "depot_org_fixture"
			args := []string{"build-a"}
			switch source {
			case "flag":
				args = append(args, "--token", token)
				t.Setenv("DEPOT_TOKEN", "ignored-environment-token")
				viper.Set("api_token", "ignored-stored-token")
			case "environment":
				t.Setenv("DEPOT_TOKEN", token)
				viper.Set("api_token", "ignored-stored-token")
			case "stored":
				viper.Set("api_token", token)
			}
			handler := &cancelBuildService{cancel: func(_ context.Context, request *connect.Request[corev1.CancelBuildRequest]) (*connect.Response[corev1.CancelBuildResponse], error) {
				if got := request.Header().Get("Authorization"); got != "Bearer "+token {
					t.Errorf("authorization = %q", got)
				}
				return connect.NewResponse(&corev1.CancelBuildResponse{Outcome: corev1.CancelBuildResponse_OUTCOME_CANCELLATION_REQUESTED}), nil
			}}
			setupCancelBuildService(t, handler)
			stdout, stderr, err := executeCancelCommand(NewCmdCancel(), context.Background(), args...)
			if source == "missing" {
				if err == nil || handler.requests.Load() != 0 {
					t.Fatalf("missing credentials: error = %v, HTTP requests = %d", err, handler.requests.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.ToLower(stdout), "cancellation requested") || strings.Contains(strings.ToLower(stdout), "stopped") {
				t.Fatalf("output = %q, want cancellation requested, without claiming stopped", stdout)
			}
			if strings.Contains(stdout+stderr, token) {
				t.Fatal("output exposed the API token")
			}
			assertCancelledBuilds(t, handler, "build-a")
		})
	}
}

func TestCancelBuildsPropagatesCancellationToInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	requestCancelled := make(chan struct{})
	handler := &cancelBuildService{cancel: func(ctx context.Context, _ *connect.Request[corev1.CancelBuildRequest]) (*connect.Response[corev1.CancelBuildResponse], error) {
		close(started)
		<-ctx.Done()
		close(requestCancelled)
		return nil, ctx.Err()
	}}
	setupCancelBuildService(t, handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, _, err := executeCancelCommand(NewCmdCancel(), ctx, "build-a", "--token", "token")
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation request never reached the server")
	}
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("interrupted command returned success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command did not return after context cancellation")
	}
	select {
	case <-requestCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("server request was not cancelled")
	}
}

func TestCancelBuildsBoundsInFlightRequests(t *testing.T) {
	var active, maximum atomic.Int32
	handler := &cancelBuildService{cancel: func(ctx context.Context, _ *connect.Request[corev1.CancelBuildRequest]) (*connect.Response[corev1.CancelBuildResponse], error) {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := maximum.Load(); current > previous; previous = maximum.Load() {
			if maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	setupCancelBuildService(t, handler)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	args := []string{"--token", "token", "--output", "json"}
	for i := range 12 {
		args = append(args, fmt.Sprintf("build-%d", i))
	}
	stdout, _, err := executeCancelCommand(NewCmdCancel(), ctx, args...)
	if err == nil {
		t.Fatal("interrupted command returned success")
	}
	if count := maximum.Load(); count != 4 {
		t.Fatalf("maximum concurrent requests = %d, want 4", count)
	}
	output := decodeCancellationOutput(t, stdout)
	if len(output.Results) != 12 {
		t.Fatalf("results = %d, want all 12 builds, including undispatched requests", len(output.Results))
	}
	for _, result := range output.Results {
		if result.Error == nil || result.Outcome != "" {
			t.Errorf("interrupted build reported success: %#v", result)
		}
	}
}

func TestCancelRunningBuildsBoundsListingAndCancellationDeadlines(t *testing.T) {
	checkDeadline := func(ctx context.Context) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 31*time.Second {
			t.Errorf("request deadline = %v, present = %v; want a positive timeout of at most 30 seconds", deadline, ok)
		}
	}
	handler := &cancelBuildService{
		list: func(ctx context.Context, _ *connect.Request[corev1.ListBuildsRequest]) (*connect.Response[corev1.ListBuildsResponse], error) {
			checkDeadline(ctx)
			return connect.NewResponse(&corev1.ListBuildsResponse{
				Builds: []*corev1.Build{{BuildId: "build-a", Status: corev1.Build_STATUS_RUNNING}},
			}), nil
		},
		cancel: func(ctx context.Context, _ *connect.Request[corev1.CancelBuildRequest]) (*connect.Response[corev1.CancelBuildResponse], error) {
			checkDeadline(ctx)
			return connect.NewResponse(&corev1.CancelBuildResponse{Outcome: corev1.CancelBuildResponse_OUTCOME_CANCELLATION_REQUESTED}), nil
		},
	}
	setupCancelBuildService(t, handler)
	_, _, err := executeCancelCommand(NewCmdCancel(), context.Background(), "--all-running", "--project", "project-a", "--yes", "--token", "token")
	if err != nil {
		t.Fatal(err)
	}
	if count := handler.requests.Load(); count != 2 {
		t.Fatalf("HTTP requests = %d, want one list and one cancel", count)
	}
}

type observedCancellationOutput struct {
	DryRun  *bool                        `json:"dry_run"`
	Results []observedCancellationResult `json:"results"`
}

type observedCancellationResult struct {
	BuildID string `json:"build_id"`
	Outcome string `json:"outcome"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeCancellationOutput(t *testing.T, stdout string) observedCancellationOutput {
	t.Helper()
	var output observedCancellationOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
	return output
}

func assertCancellationResults(t *testing.T, output observedCancellationOutput, want map[string]string) {
	t.Helper()
	got := make(map[string]string)
	for _, result := range output.Results {
		if result.Error != nil {
			t.Errorf("unexpected build error: %#v", result)
		}
		got[result.BuildID] = result.Outcome
	}
	if len(output.Results) != len(want) || !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %#v, want %v", output.Results, want)
	}
}

func executeCancelCommand(command *cobra.Command, ctx context.Context, args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	command.SetArgs(args)
	command.SetIn(strings.NewReader(""))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SilenceUsage, command.SilenceErrors = true, true
	err := command.ExecuteContext(ctx)
	return stdout.String(), stderr.String(), err
}

type cancelBuildService struct {
	corev1connect.UnimplementedBuildServiceHandler
	list     func(context.Context, *connect.Request[corev1.ListBuildsRequest]) (*connect.Response[corev1.ListBuildsResponse], error)
	cancel   func(context.Context, *connect.Request[corev1.CancelBuildRequest]) (*connect.Response[corev1.CancelBuildResponse], error)
	requests atomic.Int32
	mu       sync.Mutex
	buildIDs []string
}

func (h *cancelBuildService) ListBuilds(ctx context.Context, request *connect.Request[corev1.ListBuildsRequest]) (*connect.Response[corev1.ListBuildsResponse], error) {
	if h.list != nil {
		return h.list(ctx, request)
	}
	return connect.NewResponse(&corev1.ListBuildsResponse{}), nil
}

func (h *cancelBuildService) CancelBuild(ctx context.Context, request *connect.Request[corev1.CancelBuildRequest]) (*connect.Response[corev1.CancelBuildResponse], error) {
	h.mu.Lock()
	h.buildIDs = append(h.buildIDs, request.Msg.GetBuildId())
	h.mu.Unlock()
	if h.cancel != nil {
		return h.cancel(ctx, request)
	}
	return connect.NewResponse(&corev1.CancelBuildResponse{Outcome: corev1.CancelBuildResponse_OUTCOME_CANCELLATION_REQUESTED}), nil
}

func assertCancelledBuilds(t *testing.T, handler *cancelBuildService, want ...string) {
	t.Helper()
	handler.mu.Lock()
	got := append([]string(nil), handler.buildIDs...)
	handler.mu.Unlock()
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cancellation requests = %v, want %v", got, want)
	}
}

func setupCancelBuildService(t *testing.T, handler *cancelBuildService) {
	t.Helper()
	_, connectHandler := corev1connect.NewBuildServiceHandler(handler)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.requests.Add(1)
		connectHandler.ServeHTTP(w, r)
	}), &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
}
