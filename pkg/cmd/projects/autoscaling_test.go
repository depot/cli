package projects

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestCreateProjectAutoscaling(t *testing.T) {
	for _, test := range []struct {
		name          string
		args          []string
		wantValue     *int32
		responseValue *int32
	}{
		{name: "omitted"},
		{name: "minimum", args: []string{"--max-concurrent-builds", "1"}, wantValue: proto.Int32(1), responseValue: proto.Int32(1)},
		{name: "server response", args: []string{"--max-concurrent-builds", "3"}, wantValue: proto.Int32(3), responseValue: proto.Int32(7)},
		{name: "maximum", args: []string{"--max-concurrent-builds", "2147483647"}, wantValue: proto.Int32(2147483647), responseValue: proto.Int32(2147483647)},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := &projectServiceRecorder{createResponse: &corev1.Project{ProjectId: "project-123", MaxConcurrentBuilds: test.responseValue}}
			setupProjectService(t, handler)

			args := append([]string{"Example", "--token", "token-123"}, test.args...)
			stdout, err := executeAutoscalingCommand(NewCmdCreate(), args...)
			if err != nil {
				t.Fatal(err)
			}
			if handler.createRequest == nil {
				t.Fatal("CreateProject was not called")
			}
			got := handler.createRequest.MaxConcurrentBuilds
			if test.wantValue == nil {
				if got != nil {
					t.Fatalf("omitted flag sent max_concurrent_builds = %d", *got)
				}
			} else if got == nil || *got != *test.wantValue {
				t.Fatalf("max_concurrent_builds = %v, want %d", got, *test.wantValue)
			}
			assertAutoscalingJSON(t, stdout, test.responseValue)
			assertProjectMutationAuth(t, handler)
		})
	}
}

func TestUpdateProjectAutoscaling(t *testing.T) {
	for _, test := range []struct {
		name          string
		args          []string
		wantValue     *int32
		wantNull      bool
		responseValue *int32
	}{
		{name: "minimum", args: []string{"--max-concurrent-builds", "1"}, wantValue: proto.Int32(1), responseValue: proto.Int32(1)},
		{name: "server response", args: []string{"--max-concurrent-builds", "3"}, wantValue: proto.Int32(3), responseValue: proto.Int32(7)},
		{name: "maximum", args: []string{"--max-concurrent-builds", "2147483647"}, wantValue: proto.Int32(2147483647), responseValue: proto.Int32(2147483647)},
		{name: "disabled", args: []string{"--disable-autoscaling"}, wantNull: true},
		{name: "omitted during rename", args: []string{"--name", "Renamed"}, responseValue: proto.Int32(3)},
		{name: "false during rename", args: []string{"--name", "Renamed", "--disable-autoscaling=false"}, responseValue: proto.Int32(3)},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := &projectServiceRecorder{updateResponse: &corev1.Project{ProjectId: "project-123", MaxConcurrentBuilds: test.responseValue}}
			setupProjectService(t, handler)

			args := append([]string{"project-123", "--token", "token-123", "--output", "json"}, test.args...)
			stdout, err := executeAutoscalingCommand(NewCmdUpdate(), args...)
			if err != nil {
				t.Fatal(err)
			}
			request := handler.updateRequest
			if request == nil {
				t.Fatal("UpdateProject was not called")
			}
			if request.GetProjectId() != "project-123" {
				t.Fatalf("project_id = %q", request.GetProjectId())
			}
			switch {
			case test.wantNull:
				kind, ok := request.GetMaxConcurrentBuilds().GetKind().(*corev1.NullableInt32_Null)
				if !ok || kind.Null != structpb.NullValue_NULL_VALUE {
					t.Fatalf("disable sent max_concurrent_builds = %v, want explicit null", request.MaxConcurrentBuilds)
				}
			case test.wantValue != nil:
				kind, ok := request.GetMaxConcurrentBuilds().GetKind().(*corev1.NullableInt32_Value)
				if !ok || kind.Value != *test.wantValue {
					t.Fatalf("max_concurrent_builds = %v, want value %d", request.MaxConcurrentBuilds, *test.wantValue)
				}
			default:
				if request.MaxConcurrentBuilds != nil {
					t.Fatalf("unchanged autoscaling sent max_concurrent_builds = %v", request.MaxConcurrentBuilds)
				}
				if request.GetName() != "Renamed" {
					t.Fatalf("name = %q, want Renamed", request.GetName())
				}
			}
			if request.RegionId != nil || request.CachePolicy != nil || ((test.wantValue != nil || test.wantNull) && request.Name != nil) {
				t.Fatalf("changed unrelated project settings: %v", request)
			}
			assertAutoscalingJSON(t, stdout, test.responseValue)
			assertProjectMutationAuth(t, handler)
		})
	}
}

func TestProjectAutoscalingOutput(t *testing.T) {
	for _, test := range []struct {
		name       string
		newCommand func() *cobra.Command
		args       []string
	}{
		{name: "get JSON", newCommand: NewCmdGet, args: []string{"--output", "json"}},
		{name: "get text", newCommand: NewCmdGet},
		{name: "update text", newCommand: NewCmdUpdate, args: []string{"--name", "Renamed"}},
	} {
		for _, value := range []*int32{nil, proto.Int32(3)} {
			label := "Disabled"
			if value != nil {
				label = strconv.Itoa(int(*value))
			}
			t.Run(test.name+"/"+label, func(t *testing.T) {
				project := &corev1.Project{ProjectId: "project-123", MaxConcurrentBuilds: value}
				handler := &projectServiceRecorder{project: project, updateResponse: project}
				setupProjectService(t, handler)
				args := append([]string{"project-123", "--token", "token-123"}, test.args...)
				stdout, err := executeAutoscalingCommand(test.newCommand(), args...)
				if err != nil {
					t.Fatal(err)
				}
				if test.name == "get JSON" {
					assertAutoscalingJSON(t, stdout, value)
				} else {
					lines := strings.Split(stdout, "\n")
					found := false
					for _, line := range lines {
						if strings.HasPrefix(line, "Builds per instance:") {
							found = true
							if got := strings.TrimSpace(strings.TrimPrefix(line, "Builds per instance:")); got != label {
								t.Fatalf("Builds per instance = %q, want %q", got, label)
							}
						}
					}
					if !found {
						t.Fatalf("missing Builds per instance in output:\n%s", stdout)
					}
				}
			})
		}
	}
}

func TestProjectAutoscalingValidationBeforeAuth(t *testing.T) {
	previous := viper.GetString("api_token")
	viper.Set("api_token", "")
	t.Cleanup(func() { viper.Set("api_token", previous) })
	for _, key := range []string{"DEPOT_API_TOKEN", "DEPOT_TOKEN", "DEPOT_JIT_TOKEN", "DEPOT_CACHE_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "BUILDKITE_AGENT_ACCESS_TOKEN", "CIRCLE_OIDC_TOKEN_V2", "GITLAB_CI"} {
		t.Setenv(key, "")
	}

	for _, newCommand := range []func() *cobra.Command{NewCmdCreate, NewCmdUpdate} {
		for _, value := range []string{"0", "-1", "2147483648", "-2147483649", "1.5", "invalid"} {
			command := newCommand()
			t.Run(command.Name()+"/"+value, func(t *testing.T) {
				handler := &projectServiceRecorder{}
				setupProjectService(t, handler)
				_, err := executeAutoscalingCommand(command, "example", "--max-concurrent-builds", value)
				if err == nil || !strings.Contains(err.Error(), "max-concurrent-builds") || strings.Contains(err.Error(), "unknown flag") {
					t.Fatalf("expected invalid autoscaling value error before auth, got %v", err)
				}
				if got := handler.requestCount.Load(); got != 0 {
					t.Fatalf("invalid value made %d HTTP requests", got)
				}
			})
		}
	}

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "conflicting flags", args: []string{"--max-concurrent-builds", "3", "--disable-autoscaling"}, want: "none of the others can be"},
		{name: "conflicting explicit false", args: []string{"--max-concurrent-builds", "3", "--disable-autoscaling=false"}, want: "none of the others can be"},
		{name: "false is not a change", args: []string{"--disable-autoscaling=false"}, want: "is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := &projectServiceRecorder{}
			setupProjectService(t, handler)
			args := append([]string{"project-123"}, test.args...)
			_, err := executeAutoscalingCommand(NewCmdUpdate(), args...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error before auth, got %v", test.want, err)
			}
			if got := handler.requestCount.Load(); got != 0 {
				t.Fatalf("invalid flags made %d HTTP requests", got)
			}
		})
	}
}

func executeAutoscalingCommand(command *cobra.Command, args ...string) (string, error) {
	var stdout bytes.Buffer
	command.SetArgs(args)
	command.SetOut(&stdout)
	command.SetErr(io.Discard)
	command.SilenceUsage, command.SilenceErrors = true, true
	err := command.Execute()
	return stdout.String(), err
}

func assertAutoscalingJSON(t *testing.T, stdout string, want *int32) {
	t.Helper()
	var output map[string]any
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout)
	}
	got, exists := output["max_concurrent_builds"]
	if !exists {
		t.Fatalf("JSON output omitted max_concurrent_builds:\n%s", stdout)
	}
	if want == nil {
		if got != nil {
			t.Fatalf("max_concurrent_builds = %#v, want null", got)
		}
	} else if got != float64(*want) {
		t.Fatalf("max_concurrent_builds = %#v, want JSON number %d", got, *want)
	}
}

func assertProjectMutationAuth(t *testing.T, handler *projectServiceRecorder) {
	t.Helper()
	if got := handler.requestCount.Load(); got != 1 {
		t.Fatalf("HTTP requests = %d, want 1", got)
	}
	if handler.authorization != "Bearer token-123" {
		t.Fatalf("Authorization = %q, want Bearer token-123", handler.authorization)
	}
}
