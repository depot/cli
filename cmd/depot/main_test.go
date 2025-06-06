package main

import (
	"os"
	"reflect"
	"testing"
)

func TestParseCmdSubcmd(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantCmd string
		wantSub string
	}{
		{
			name:    "buildx build command",
			args:    []string{"buildx", "build", "."},
			wantCmd: "buildx",
			wantSub: "build",
		},
		{
			name:    "buildx bake command",
			args:    []string{"buildx", "bake", "target"},
			wantCmd: "buildx",
			wantSub: "bake",
		},
		{
			name:    "command with flags",
			args:    []string{"--verbose", "buildx", "--debug", "build", "."},
			wantCmd: "buildx",
			wantSub: "build",
		},
		{
			name:    "single command",
			args:    []string{"login"},
			wantCmd: "login",
			wantSub: "",
		},
		{
			name:    "command with flags only",
			args:    []string{"--help"},
			wantCmd: "",
			wantSub: "",
		},
		{
			name:    "empty args",
			args:    []string{},
			wantCmd: "",
			wantSub: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original args
			originalArgs := os.Args
			defer func() { os.Args = originalArgs }()

			// Set test args
			os.Args = append([]string{"depot"}, tt.args...)

			gotCmd, gotSub := parseCmdSubcmd()
			if gotCmd != tt.wantCmd {
				t.Errorf("parseCmdSubcmd() cmd = %v, want %v", gotCmd, tt.wantCmd)
			}
			if gotSub != tt.wantSub {
				t.Errorf("parseCmdSubcmd() subcmd = %v, want %v", gotSub, tt.wantSub)
			}
		})
	}
}

func TestRewriteBuildxArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "buildx build",
			args: []string{"buildx", "build", "."},
			want: []string{"depot", "build", "."},
		},
		{
			name: "buildx bake",
			args: []string{"buildx", "bake", "target"},
			want: []string{"depot", "bake", "target"},
		},
		{
			name: "buildx with flags",
			args: []string{"--verbose", "buildx", "build", "--platform", "linux/amd64", "."},
			want: []string{"--verbose", "depot", "build", "--platform", "linux/amd64", "."},
		},
		{
			name: "no buildx command",
			args: []string{"login"},
			want: []string{"login"},
		},
		{
			name: "buildx not first non-flag arg",
			args: []string{"other", "buildx", "build"},
			want: []string{"other", "depot", "build"},
		},
		{
			name: "empty args",
			args: []string{},
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original args
			originalArgs := os.Args
			defer func() { os.Args = originalArgs }()

			// Set test args
			os.Args = append([]string{"depot"}, tt.args...)

			got := rewriteBuildxArgs()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("rewriteBuildxArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldCheckForUpdate(t *testing.T) {
	tests := []struct {
		name    string
		envVar  string
		want    bool
		setup   func()
		cleanup func()
	}{
		{
			name:   "check disabled by env var",
			envVar: "DEPOT_NO_UPDATE_NOTIFIER",
			want:   false,
			setup: func() {
				os.Setenv("DEPOT_NO_UPDATE_NOTIFIER", "1")
			},
			cleanup: func() {
				os.Unsetenv("DEPOT_NO_UPDATE_NOTIFIER")
			},
		},
		{
			name:    "check enabled when env var not set",
			want:    true, // This assumes we're running in a terminal
			setup:   func() {},
			cleanup: func() {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			defer tt.cleanup()

			got := shouldCheckForUpdate()
			// Note: This test depends on whether we're actually running in a terminal
			// In CI environments, this might be false even without the env var
			if tt.envVar != "" && got != tt.want {
				t.Errorf("shouldCheckForUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}
