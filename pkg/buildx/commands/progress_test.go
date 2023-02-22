package commands

import (
	"reflect"
	"testing"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		wantCmd   Command
		wantFound bool
	}{
		{
			name:      "empty",
			command:   "",
			wantCmd:   Command{},
			wantFound: false,
		},
		{
			name:      "internal is not a command",
			command:   "[internal] load build definition from Dockerfile",
			wantCmd:   Command{},
			wantFound: false,
		},
		{
			name:    "no platform and no stage",
			command: "[ 1/20] FROM goller/howdy:latest",
			wantCmd: Command{
				Step:  1,
				Total: 20,
			},
			wantFound: true,
		},
		{
			name:    "no platform",
			command: "[stage-1  1/20] FROM goller/howdy:latest as builder",
			wantCmd: Command{
				Stage: "stage-1",
				Step:  1,
				Total: 20,
			},
			wantFound: true,
		},
		{
			name:    "no stage",
			command: "[linux/amd64  1/20] FROM goller/howdy:latest as builder",
			wantCmd: Command{
				Platform: "linux/amd64",
				Step:     1,
				Total:    20,
			},
			wantFound: true,
		},
		{
			name:    "platform and stage",
			command: "[linux/amd64 stage-1  1/20] FROM goller/howdy:latest as builder",
			wantCmd: Command{
				Platform: "linux/amd64",
				Stage:    "stage-1",
				Step:     1,
				Total:    20,
			},
			wantFound: true,
		},
		{
			name:      "SBOM has no steps",
			command:   "[linux/riscv64] generating sbom using docker.io/docker/buildkit-syft-scanner:stable-1",
			wantCmd:   Command{},
			wantFound: false,
		},
		{
			name:    "emulator of ppc64le",
			command: "[linux/arm64->ppc64le buildkitd 1/1] RUN --mount=target=. --mount=target=/root/.cache,type=cache   --mount=target=/go/p...",
			wantCmd: Command{
				Platform: "linux/arm64->ppc64le",
				Stage:    "buildkitd",
				Step:     1,
				Total:    1,
			},
			wantFound: true,
		},
		{
			name:    "specific arm versions",
			command: "[linux/arm/v7 binaries-linux 1/2] COPY --link --from=buildctl /usr/bin/buildctl /",
			wantCmd: Command{
				Platform: "linux/arm/v7",
				Stage:    "binaries-linux",
				Step:     1,
				Total:    2,
			},
			wantFound: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCmd, gotFound := ParseCommand(tt.command)
			if !reflect.DeepEqual(gotCmd, tt.wantCmd) {
				t.Errorf("ParseCommand() gotCmd = %v, want %v", gotCmd, tt.wantCmd)
			}
			if gotFound != tt.wantFound {
				t.Errorf("ParseCommand() gotFound = %v, want %v", gotFound, tt.wantFound)
			}
		})
	}
}
