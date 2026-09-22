package pull

import (
	"testing"

	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
)

func TestIsDepotRegistryReference(t *testing.T) {
	cases := map[string]bool{
		"registry.depot.dev/abc123:latest":         true,
		"acmeorg.registry.depot.dev/abc123:latest": true,
		"abc123:latest":                             false,
		"abc123":                                    false,
		"ghcr.io/depot/cli:latest":                  false,
		"notregistry.depot.dev/abc123:latest":       false,
		"registry.depot.dev.example.com/abc:latest": false,
	}
	for ref, want := range cases {
		if got := isDepotRegistryReference(ref); got != want {
			t.Errorf("isDepotRegistryReference(%q) = %v, want %v", ref, got, want)
		}
	}
}

func TestExtractProjectIDAndTag(t *testing.T) {
	cases := []struct {
		ref       string
		projectID string
		tag       string
	}{
		{"registry.depot.dev/abc123:v1", "abc123", "v1"},
		{"acmeorg.registry.depot.dev/abc123:v1", "abc123", "v1"},
		{"abc123:v1", "abc123", "v1"},
		{"registry.depot.dev/abc123", "", "registry.depot.dev/abc123"},
	}
	for _, tc := range cases {
		projectID, tag := extractProjectIDAndTag(tc.ref)
		if projectID != tc.projectID || tag != tc.tag {
			t.Errorf("extractProjectIDAndTag(%q) = (%q, %q), want (%q, %q)", tc.ref, projectID, tag, tc.projectID, tc.tag)
		}
	}
}

func TestRehostReference(t *testing.T) {
	cases := []struct{ ref, host, want string }{
		{"registry.depot.dev/abc123:bld1", "acmeorg.registry.depot.dev", "acmeorg.registry.depot.dev/abc123:bld1"},
		{"registry.depot.dev/abc123:bld1", "registry.depot.dev", "registry.depot.dev/abc123:bld1"},
		{"other.example.com/abc123:bld1", "acmeorg.registry.depot.dev", "other.example.com/abc123:bld1"},
	}
	for _, tc := range cases {
		if got := rehostReference(tc.ref, tc.host); got != tc.want {
			t.Errorf("rehostReference(%q, %q) = %q, want %q", tc.ref, tc.host, got, tc.want)
		}
	}
}

func TestBuildPullOptUsesOrgRegistryHost(t *testing.T) {
	msg := &cliv1.GetPullInfoResponse{
		Reference:    "registry.depot.dev/abc123:bld1",
		Username:     "x-token",
		Password:     "secret",
		RegistryHost: "acmeorg.registry.depot.dev",
	}
	p := buildPullOpt(msg, nil, "", "auto")
	if p.imageName != "acmeorg.registry.depot.dev/abc123:bld1" {
		t.Errorf("imageName = %q", p.imageName)
	}
	if *p.pullOptions.ServerAddress != "acmeorg.registry.depot.dev" {
		t.Errorf("ServerAddress = %q", *p.pullOptions.ServerAddress)
	}
}

func TestBuildPullOptFallsBackToLegacyHost(t *testing.T) {
	msg := &cliv1.GetPullInfoResponse{
		Reference: "registry.depot.dev/abc123:bld1",
		Username:  "x-token",
		Password:  "secret",
	}
	p := buildPullOpt(msg, nil, "", "auto")
	if p.imageName != "registry.depot.dev/abc123:bld1" {
		t.Errorf("imageName = %q", p.imageName)
	}
	if *p.pullOptions.ServerAddress != "registry.depot.dev" {
		t.Errorf("ServerAddress = %q", *p.pullOptions.ServerAddress)
	}
}

func TestBakePullOptsUseOrgRegistryHost(t *testing.T) {
	app, db := "app", "db"
	msg := &cliv1.GetPullInfoResponse{
		Reference:    "registry.depot.dev/abc123:bld1",
		Username:     "x-token",
		Password:     "secret",
		RegistryHost: "acmeorg.registry.depot.dev",
		Options: []*cliv1.BuildOptions{
			{Command: cliv1.Command_COMMAND_BAKE, TargetName: &app},
			{Command: cliv1.Command_COMMAND_BAKE, TargetName: &db},
		},
	}
	pulls := bakePullOpts(msg, nil, []string{"myimage"}, "", "auto")
	if len(pulls) != 2 {
		t.Fatalf("expected 2 pulls, got %d", len(pulls))
	}
	want := map[string]string{
		"acmeorg.registry.depot.dev/abc123:bld1-app": "myimage-app",
		"acmeorg.registry.depot.dev/abc123:bld1-db":  "myimage-db",
	}
	for _, p := range pulls {
		tag, ok := want[p.imageName]
		if !ok {
			t.Errorf("unexpected imageName %q", p.imageName)
			continue
		}
		if len(p.pullOptions.UserTags) != 1 || p.pullOptions.UserTags[0] != tag {
			t.Errorf("UserTags for %q = %v, want [%s]", p.imageName, p.pullOptions.UserTags, tag)
		}
		if *p.pullOptions.ServerAddress != "acmeorg.registry.depot.dev" {
			t.Errorf("ServerAddress for %q = %q", p.imageName, *p.pullOptions.ServerAddress)
		}
	}
}
