package push

import (
	"testing"

	"github.com/containerd/containerd/reference"
)

func TestParseTag(t *testing.T) {
	tests := []struct {
		name    string
		tag     string
		want    *ParsedTag
		wantErr bool
	}{
		{
			name: "docker hub image with tag",
			tag:  "myuser/myapp:v1.0",
			want: &ParsedTag{
				Host:    "registry-1.docker.io",
				Path:    "myuser/myapp",
				Refspec: mustParseRef("docker.io/myuser/myapp:v1.0"),
				Tag:     "v1.0",
			},
		},
		{
			name: "docker hub image without tag defaults to latest",
			tag:  "myuser/myapp",
			want: &ParsedTag{
				Host:    "registry-1.docker.io",
				Path:    "myuser/myapp",
				Refspec: mustParseRef("docker.io/myuser/myapp:latest"),
				Tag:     "latest",
			},
		},
		{
			name: "custom registry with port",
			tag:  "my-registry.com:5000/namespace/app:dev",
			want: &ParsedTag{
				Host:    "my-registry.com:5000",
				Path:    "namespace/app",
				Refspec: mustParseRef("my-registry.com:5000/namespace/app:dev"),
				Tag:     "dev",
			},
		},
		{
			name: "gcr registry",
			tag:  "gcr.io/my-project/app:staging",
			want: &ParsedTag{
				Host:    "gcr.io",
				Path:    "my-project/app",
				Refspec: mustParseRef("gcr.io/my-project/app:staging"),
				Tag:     "staging",
			},
		},
		{
			name: "ecr registry",
			tag:  "123456789012.dkr.ecr.us-west-2.amazonaws.com/myapp:latest",
			want: &ParsedTag{
				Host:    "123456789012.dkr.ecr.us-west-2.amazonaws.com",
				Path:    "myapp",
				Refspec: mustParseRef("123456789012.dkr.ecr.us-west-2.amazonaws.com/myapp:latest"),
				Tag:     "latest",
			},
		},
		{
			name: "localhost registry",
			tag:  "localhost:5000/test:v1",
			want: &ParsedTag{
				Host:    "localhost:5000",
				Path:    "test",
				Refspec: mustParseRef("localhost:5000/test:v1"),
				Tag:     "v1",
			},
		},
		{
			name: "library image on docker hub",
			tag:  "nginx:alpine",
			want: &ParsedTag{
				Host:    "registry-1.docker.io",
				Path:    "library/nginx",
				Refspec: mustParseRef("docker.io/library/nginx:alpine"),
				Tag:     "alpine",
			},
		},
		{
			name: "complex tag with build number",
			tag:  "myregistry.io/team/app:feature-branch-build-123",
			want: &ParsedTag{
				Host:    "myregistry.io",
				Path:    "team/app",
				Refspec: mustParseRef("myregistry.io/team/app:feature-branch-build-123"),
				Tag:     "feature-branch-build-123",
			},
		},
		{
			name:    "invalid tag format",
			tag:     "invalid:tag:format:too:many:colons",
			wantErr: true,
		},
		{
			name:    "empty tag",
			tag:     "",
			wantErr: true,
		},
		{
			name: "tag with uppercase characters (actually valid)",
			tag:  "my-registry.com/app:INVALID_TAG",
			want: &ParsedTag{
				Host:    "my-registry.com",
				Path:    "app",
				Refspec: mustParseRef("my-registry.com/app:INVALID_TAG"),
				Tag:     "INVALID_TAG",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTag(tt.tag)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseTag() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				// Compare fields individually for better error reporting
				if got.Host != tt.want.Host {
					t.Errorf("ParseTag() Host = %v, want %v", got.Host, tt.want.Host)
				}
				if got.Path != tt.want.Path {
					t.Errorf("ParseTag() Path = %v, want %v", got.Path, tt.want.Path)
				}
				if got.Tag != tt.want.Tag {
					t.Errorf("ParseTag() Tag = %v, want %v", got.Tag, tt.want.Tag)
				}
				if got.Refspec.String() != tt.want.Refspec.String() {
					t.Errorf("ParseTag() Refspec = %v, want %v", got.Refspec.String(), tt.want.Refspec.String())
				}
			}
		})
	}
}

func TestParsedTagStructure(t *testing.T) {
	// Test that ParsedTag structure works as expected
	tag := &ParsedTag{
		Host:    "registry.example.com",
		Path:    "namespace/app",
		Tag:     "v1.0",
		Refspec: mustParseRef("registry.example.com/namespace/app:v1.0"),
	}

	if tag.Host != "registry.example.com" {
		t.Error("Host field not working correctly")
	}

	if tag.Path != "namespace/app" {
		t.Error("Path field not working correctly")
	}

	if tag.Tag != "v1.0" {
		t.Error("Tag field not working correctly")
	}

	if tag.Refspec.String() != "registry.example.com/namespace/app:v1.0" {
		t.Error("Refspec field not working correctly")
	}
}

// Helper function to create reference.Spec for tests
func mustParseRef(s string) reference.Spec {
	ref, err := reference.Parse(s)
	if err != nil {
		panic(err)
	}
	return ref
}