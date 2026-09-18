package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProjectsCommandRegistration(t *testing.T) {
	command := NewCmdProjects()
	registered := make(map[string]bool)
	for _, subcommand := range command.Commands() {
		registered[subcommand.Name()] = true
	}

	for _, name := range []string{"create", "delete", "get", "list", "update"} {
		if !registered[name] {
			t.Errorf("subcommand %q not registered under `depot projects`", name)
		}
	}
}

func TestCreateProjectSendsCachePolicyKeepDays(t *testing.T) {
	handler := &projectServiceRecorder{}
	setupProjectService(t, handler)

	var stdout bytes.Buffer
	command := NewCmdCreate()
	command.SetArgs([]string{
		"Example",
		"--organization", "org-123",
		"--region", "eu-central-1",
		"--cache-storage-policy", "75",
		"--cache-policy-keep-days", "30",
		"--token", "token-123",
	})
	command.SetOut(&stdout)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SilenceErrors = true

	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	request := handler.createRequest
	if request == nil {
		t.Fatal("CreateProject was not called")
	}
	if request.GetName() != "Example" || request.GetOrganizationId() != "org-123" {
		t.Fatalf("request = %#v", request)
	}
	if request.GetRegionId() != "eu-central-1" {
		t.Fatalf("RegionId = %q, want eu-central-1", request.GetRegionId())
	}
	if request.GetCachePolicy().GetKeepBytes() != 75*bytesPerGigabyte {
		t.Fatalf("CachePolicy.KeepBytes = %d, want %d", request.GetCachePolicy().GetKeepBytes(), 75*bytesPerGigabyte)
	}
	if request.GetCachePolicy().GetKeepDays() != 30 {
		t.Fatalf("CachePolicy.KeepDays = %d, want 30", request.GetCachePolicy().GetKeepDays())
	}
	if handler.authorization != "Bearer token-123" {
		t.Fatalf("Authorization = %q, want Bearer token-123", handler.authorization)
	}
}

func TestCreateProjectDefaultsCachePolicyKeepDays(t *testing.T) {
	handler := &projectServiceRecorder{}
	setupProjectService(t, handler)

	command := NewCmdCreate()
	command.SetArgs([]string{"Example", "--token", "token-123"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SilenceErrors = true

	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	if handler.createRequest == nil {
		t.Fatal("CreateProject was not called")
	}
	if got := handler.createRequest.GetCachePolicy().GetKeepDays(); got != 14 {
		t.Fatalf("CachePolicy.KeepDays = %d, want 14", got)
	}
}

func TestCreateProjectRejectsNegativeCachePolicyKeepDays(t *testing.T) {
	command := NewCmdCreate()
	command.SetArgs([]string{"Example", "--cache-policy-keep-days", "-1"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SilenceErrors = true

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "--cache-policy-keep-days cannot be negative") {
		t.Fatalf("error = %v", err)
	}
}

func TestGetProjectCallsAPIAndWritesJSON(t *testing.T) {
	handler := &projectServiceRecorder{
		project: &corev1.Project{
			ProjectId:      "project-123",
			OrganizationId: "org-123",
			Name:           "Example",
			RegionId:       "eu-central-1",
			CreatedAt:      timestamppb.New(time.Date(2026, time.July, 29, 12, 30, 0, 0, time.UTC)),
			CachePolicy: &corev1.CachePolicy{
				KeepBytes: 50 * bytesPerGigabyte,
				KeepDays:  14,
			},
		},
	}
	setupProjectService(t, handler)

	var stdout bytes.Buffer
	command := NewCmdGet()
	command.SetArgs([]string{"project-123", "--token", "token-123", "--output", "json"})
	command.SetOut(&stdout)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SilenceErrors = true

	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	if handler.getRequest == nil || handler.getRequest.GetProjectId() != "project-123" {
		t.Fatalf("GetProject request = %#v, want project-123", handler.getRequest)
	}
	if handler.authorization != "Bearer token-123" {
		t.Fatalf("Authorization = %q, want Bearer token-123", handler.authorization)
	}

	var output projectOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if output.ProjectID != "project-123" || output.OrganizationID != "org-123" {
		t.Fatalf("output = %#v", output)
	}
	if output.CreatedAt != "2026-07-29T12:30:00Z" {
		t.Fatalf("created_at = %q", output.CreatedAt)
	}
	if output.CachePolicy == nil || output.CachePolicy.GetKeepDays() != 14 {
		t.Fatalf("cache_policy = %#v", output.CachePolicy)
	}
}

func TestUpdateProjectSendsOnlyChangedFields(t *testing.T) {
	handler := &projectServiceRecorder{
		project: &corev1.Project{
			ProjectId: "project-123",
			CachePolicy: &corev1.CachePolicy{
				KeepBytes: 50 * bytesPerGigabyte,
				KeepDays:  14,
			},
		},
	}
	setupProjectService(t, handler)

	var stdout bytes.Buffer
	command := NewCmdUpdate()
	command.SetArgs([]string{
		"--project-id", "project-123",
		"--name", "Renamed",
		"--cache-storage-policy", "75",
		"--token", "token-123",
		"--output", "json",
	})
	command.SetOut(&stdout)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SilenceErrors = true

	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	request := handler.updateRequest
	if request == nil {
		t.Fatal("UpdateProject was not called")
	}
	if request.GetProjectId() != "project-123" {
		t.Fatalf("ProjectId = %q", request.GetProjectId())
	}
	if request.Name == nil || request.GetName() != "Renamed" {
		t.Fatalf("Name = %#v", request.Name)
	}
	if request.RegionId != nil {
		t.Fatalf("RegionId = %#v, want nil", request.RegionId)
	}
	if request.CachePolicy == nil || request.CachePolicy.GetKeepBytes() != 75*bytesPerGigabyte {
		t.Fatalf("CachePolicy = %#v", request.CachePolicy)
	}
	if request.CachePolicy.GetKeepDays() != 14 {
		t.Fatalf("CachePolicy.KeepDays = %d, want 14", request.CachePolicy.GetKeepDays())
	}
	if handler.getRequest == nil || handler.getRequest.GetProjectId() != "project-123" {
		t.Fatalf("GetProject request = %#v, want project-123", handler.getRequest)
	}
	if handler.authorization != "Bearer token-123" {
		t.Fatalf("Authorization = %q, want Bearer token-123", handler.authorization)
	}

	var output projectOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if output.ProjectID != "project-123" || output.Name != "Renamed" {
		t.Fatalf("output = %#v", output)
	}
}

func TestUpdateProjectRequiresAChange(t *testing.T) {
	command := NewCmdUpdate()
	command.SetArgs([]string{"project-123"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SilenceErrors = true

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "one of --name, --region, or --cache-storage-policy is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestProjectCommandsRejectArgumentAndProjectIDFlag(t *testing.T) {
	command := NewCmdGet()
	command.SetArgs([]string{"project-123", "--project-id", "project-456"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SilenceErrors = true

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "either an argument or --project-id, not both") {
		t.Fatalf("error = %v", err)
	}
}

func TestGetProjectRejectsUnsupportedOutputBeforeAuth(t *testing.T) {
	command := NewCmdGet()
	command.SetArgs([]string{"project-123", "--output", "yaml"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SilenceErrors = true

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), `unsupported output "yaml"`) {
		t.Fatalf("error = %v", err)
	}
}

type projectServiceRecorder struct {
	corev1connect.UnimplementedProjectServiceHandler

	project       *corev1.Project
	createRequest *corev1.CreateProjectRequest
	getRequest    *corev1.GetProjectRequest
	updateRequest *corev1.UpdateProjectRequest
	authorization string
}

func (h *projectServiceRecorder) CreateProject(_ context.Context, request *connect.Request[corev1.CreateProjectRequest]) (*connect.Response[corev1.CreateProjectResponse], error) {
	h.createRequest = request.Msg
	h.authorization = request.Header().Get("Authorization")
	return connect.NewResponse(&corev1.CreateProjectResponse{
		Project: &corev1.Project{
			ProjectId:      "project-123",
			OrganizationId: request.Msg.GetOrganizationId(),
			Name:           request.Msg.GetName(),
			RegionId:       request.Msg.GetRegionId(),
			CachePolicy:    request.Msg.GetCachePolicy(),
		},
	}), nil
}

func (h *projectServiceRecorder) GetProject(_ context.Context, request *connect.Request[corev1.GetProjectRequest]) (*connect.Response[corev1.GetProjectResponse], error) {
	h.getRequest = request.Msg
	h.authorization = request.Header().Get("Authorization")
	return connect.NewResponse(&corev1.GetProjectResponse{Project: h.project}), nil
}

func (h *projectServiceRecorder) UpdateProject(_ context.Context, request *connect.Request[corev1.UpdateProjectRequest]) (*connect.Response[corev1.UpdateProjectResponse], error) {
	h.updateRequest = request.Msg
	h.authorization = request.Header().Get("Authorization")
	return connect.NewResponse(&corev1.UpdateProjectResponse{
		Project: &corev1.Project{
			ProjectId:   request.Msg.GetProjectId(),
			Name:        request.Msg.GetName(),
			RegionId:    request.Msg.GetRegionId(),
			CachePolicy: request.Msg.GetCachePolicy(),
		},
	}), nil
}

func setupProjectService(t *testing.T, handler corev1connect.ProjectServiceHandler) {
	t.Helper()

	_, connectHandler := corev1connect.NewProjectServiceHandler(handler)
	server := httptest.NewServer(h2c.NewHandler(connectHandler, &http2.Server{}))
	t.Cleanup(server.Close)
	t.Setenv("DEPOT_API_URL", server.URL)
}
