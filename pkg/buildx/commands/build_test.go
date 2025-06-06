package commands

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/docker/buildx/build"
)

func TestParseInvokeConfig(t *testing.T) {
	tests := []struct {
		name    string
		invoke  string
		want    build.ContainerConfig
		wantErr bool
	}{
		{
			name:   "default config",
			invoke: "default",
			want:   build.ContainerConfig{Tty: true},
		},
		{
			name:   "simple command",
			invoke: "sh",
			want:   build.ContainerConfig{Tty: true, Cmd: []string{"sh"}},
		},
		{
			name:   "command with args",
			invoke: "args=echo hello",
			want:   build.ContainerConfig{Tty: true, Cmd: []string{"echo hello"}},
		},
		{
			name:   "multiple fields",
			invoke: "args=sh,user=root,env=FOO=bar",
			want: build.ContainerConfig{
				Tty:  true,
				Cmd:  []string{"sh"},
				User: stringPtr("root"),
				Env:  []string{"FOO=bar"},
			},
		},
		{
			name:   "entrypoint and cwd",
			invoke: "entrypoint=/bin/sh,cwd=/app",
			want: build.ContainerConfig{
				Tty:        true,
				Entrypoint: []string{"/bin/sh"},
				Cwd:        stringPtr("/app"),
			},
		},
		{
			name:   "tty false",
			invoke: "tty=false",
			want:   build.ContainerConfig{Tty: false},
		},
		{
			name:    "invalid tty value",
			invoke:  "tty=invalid",
			wantErr: true,
		},
		{
			name:    "invalid field",
			invoke:  "invalid=value",
			wantErr: true,
		},
		{
			name:   "single field without equals treated as command",
			invoke: "noequals",
			want:   build.ContainerConfig{Tty: true, Cmd: []string{"noequals"}},
		},
		{
			name:    "invalid field without equals in multi-field",
			invoke:  "user=root,invalidfield",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseInvokeConfig(tt.invoke)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseInvokeConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseInvokeConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestListToMap(t *testing.T) {
	tests := []struct {
		name       string
		values     []string
		defaultEnv bool
		want       map[string]string
		setup      func()
		cleanup    func()
	}{
		{
			name:       "key-value pairs",
			values:     []string{"KEY1=value1", "KEY2=value2"},
			defaultEnv: false,
			want:       map[string]string{"KEY1": "value1", "KEY2": "value2"},
			setup:      func() {},
			cleanup:    func() {},
		},
		{
			name:       "key without value",
			values:     []string{"KEY1", "KEY2=value2"},
			defaultEnv: false,
			want:       map[string]string{"KEY1": "", "KEY2": "value2"},
			setup:      func() {},
			cleanup:    func() {},
		},
		{
			name:       "key without value with defaultEnv",
			values:     []string{"TEST_VAR", "KEY2=value2"},
			defaultEnv: true,
			want:       map[string]string{"TEST_VAR": "test_value", "KEY2": "value2"},
			setup: func() {
				t.Setenv("TEST_VAR", "test_value")
			},
			cleanup: func() {},
		},
		{
			name:       "empty values",
			values:     []string{},
			defaultEnv: false,
			want:       map[string]string{},
			setup:      func() {},
			cleanup:    func() {},
		},
		{
			name:       "value with equals sign",
			values:     []string{"KEY1=value=with=equals"},
			defaultEnv: false,
			want:       map[string]string{"KEY1": "value=with=equals"},
			setup:      func() {},
			cleanup:    func() {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			defer tt.cleanup()
			
			got := listToMap(tt.values, tt.defaultEnv)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("listToMap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseContextNames(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		want    map[string]build.NamedContext
		wantErr bool
	}{
		{
			name:   "empty values",
			values: []string{},
			want:   nil,
		},
		{
			name:   "single context",
			values: []string{"mycontext=/path/to/context"},
			want: map[string]build.NamedContext{
				"mycontext": {Path: "/path/to/context"},
			},
		},
		{
			name:   "multiple contexts",
			values: []string{"ctx1=/path1", "ctx2=/path2"},
			want: map[string]build.NamedContext{
				"ctx1": {Path: "/path1"},
				"ctx2": {Path: "/path2"},
			},
		},
		{
			name:   "image reference context",
			values: []string{"alpine=docker-image://alpine:latest"},
			want: map[string]build.NamedContext{
				"alpine": {Path: "docker-image://alpine:latest"},
			},
		},
		{
			name:    "invalid format",
			values:  []string{"invalid"},
			wantErr: true,
		},
		{
			name:    "invalid context name",
			values:  []string{"@invalid=/path"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseContextNames(tt.values)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseContextNames() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseContextNames() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParsePrintFunc(t *testing.T) {
	tests := []struct {
		name    string
		str     string
		want    *build.PrintFunc
		wantErr bool
	}{
		{
			name: "empty string",
			str:  "",
			want: nil,
		},
		{
			name: "simple name",
			str:  "outline",
			want: &build.PrintFunc{Name: "outline"},
		},
		{
			name: "name with format",
			str:  "outline,format=json",
			want: &build.PrintFunc{Name: "outline", Format: "json"},
		},
		{
			name: "format only",
			str:  "format=json",
			want: &build.PrintFunc{Format: "json"},
		},
		{
			name:    "invalid field",
			str:     "invalid=value",
			wantErr: true,
		},
		{
			name:    "multiple names",
			str:     "name1,name2",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePrintFunc(tt.str)
			if (err != nil) != tt.wantErr {
				t.Errorf("parsePrintFunc() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parsePrintFunc() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldRetryError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "inconsistent graph state error",
			err:  errors.New("inconsistent graph state"),
			want: true,
		},
		{
			name: "failed to get state for index error",
			err:  errors.New("failed to get state for index"),
			want: true,
		},
		{
			name: "other error",
			err:  errors.New("some other error"),
			want: false,
		},
		{
			name: "network timeout error",
			err:  errors.New("network timeout"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRetryError(tt.err)
			if got != tt.want {
				t.Errorf("shouldRetryError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRewriteFriendlyErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil error",
			err:  nil,
			want: "",
		},
		{
			name: "dockerignore error",
			err:  errors.New(`header key "exclude-patterns" contains value with non-printable ASCII characters`),
			want: `header key "exclude-patterns" contains value with non-printable ASCII characters. Please check your .dockerignore file for invalid characters.`,
		},
		{
			name: "checksum error",
			err:  errors.New("failed to solve: failed to compute cache key: failed to calculate checksum of ref context::somefile: file does not exist"),
			want: " file does not exist. Please check if the files exist in the context.",
		},
		{
			name: "grpc canceled error",
			err:  errors.New("code = Canceled desc = grpc: the client connection is closing"),
			want: "build canceled",
		},
		{
			name: "other error unchanged",
			err:  errors.New("some other error"),
			want: "some other error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteFriendlyErrors(tt.err)
			if tt.err == nil {
				if got != nil {
					t.Errorf("rewriteFriendlyErrors() = %v, want nil", got)
				}
			} else {
				if got.Error() != tt.want {
					t.Errorf("rewriteFriendlyErrors() = %v, want %v", got.Error(), tt.want)
				}
			}
		})
	}
}

func TestRetryRetryableErrors(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func() func() error
		wantCallCount int
		wantErr       bool
	}{
		{
			name: "no error",
			setupFunc: func() func() error {
				return func() error { return nil }
			},
			wantCallCount: 1,
			wantErr:       false,
		},
		{
			name: "non-retryable error",
			setupFunc: func() func() error {
				return func() error { return errors.New("non-retryable error") }
			},
			wantCallCount: 1,
			wantErr:       true,
		},
		{
			name: "retryable error that succeeds on retry",
			setupFunc: func() func() error {
				callCount := 0
				return func() error {
					callCount++
					if callCount == 1 {
						return errors.New("inconsistent graph state")
					}
					return nil
				}
			},
			wantCallCount: 2,
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callCount := 0
			f := tt.setupFunc()
			wrappedF := func() error {
				callCount++
				return f()
			}

			err := retryRetryableErrors(context.Background(), wrappedF)
			if (err != nil) != tt.wantErr {
				t.Errorf("retryRetryableErrors() error = %v, wantErr %v", err, tt.wantErr)
			}
			if callCount != tt.wantCallCount {
				t.Errorf("retryRetryableErrors() callCount = %v, want %v", callCount, tt.wantCallCount)
			}
		})
	}
}

// Helper function for pointer to string
func stringPtr(s string) *string {
	return &s
}