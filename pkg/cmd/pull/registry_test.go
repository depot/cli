package pull

import (
	"testing"

	"github.com/depot/cli/pkg/load"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
)

func TestRegistryHostFallsBackToSharedHost(t *testing.T) {
	if got := registryHost("5m6p3sp5rj.registry.depot.dev"); got != "5m6p3sp5rj.registry.depot.dev" {
		t.Fatalf("unexpected host: %s", got)
	}
	if got := registryHost(""); got != "registry.depot.dev" {
		t.Fatalf("unexpected host: %s", got)
	}
}

func TestSplitRegistryReference(t *testing.T) {
	for _, test := range []struct {
		reference string
		projectID string
		tag       string
		ok        bool
	}{
		{reference: "855xt0289s:sweeping-svm", projectID: "855xt0289s", tag: "sweeping-svm", ok: true},
		{reference: "registry.depot.dev/855xt0289s:sweeping-svm", projectID: "855xt0289s", tag: "sweeping-svm", ok: true},
		{
			reference: "5m6p3sp5rj.registry.depot.dev/855xt0289s:sweeping-svm",
			projectID: "855xt0289s",
			tag:       "sweeping-svm",
			ok:        true,
		},
		{reference: "855xt0289s"},
		{reference: "ghcr.io/depot/cli:latest"},
		{reference: ":sweeping-svm"},
		{reference: "855xt0289s:"},
	} {
		projectID, tag, ok := splitRegistryReference(test.reference)
		if ok != test.ok || projectID != test.projectID || tag != test.tag {
			t.Errorf("splitRegistryReference(%q) = (%q, %q, %t), want (%q, %q, %t)",
				test.reference, projectID, tag, ok, test.projectID, test.tag, test.ok)
		}
	}
}

func TestRewriteReferenceHost(t *testing.T) {
	const orgHost = "5m6p3sp5rj.registry.depot.dev"

	for _, test := range []struct {
		reference string
		want      string
	}{
		{reference: "registry.depot.dev/855xt0289s:16ae9e5", want: orgHost + "/855xt0289s:16ae9e5"},
		{reference: orgHost + "/855xt0289s:16ae9e5", want: orgHost + "/855xt0289s:16ae9e5"},
		{reference: "ghcr.io/depot/cli:latest", want: "ghcr.io/depot/cli:latest"},
		{reference: "855xt0289s:16ae9e5", want: "855xt0289s:16ae9e5"},
	} {
		if got := rewriteReferenceHost(test.reference, orgHost); got != test.want {
			t.Errorf("rewriteReferenceHost(%q) = %q, want %q", test.reference, got, test.want)
		}
	}
}

func TestBuildPullOptUsesOrgRegistryHost(t *testing.T) {
	msg := &cliv1.GetPullInfoResponse{
		Reference:    "registry.depot.dev/855xt0289s:16ae9e5",
		Username:     "x-token",
		Password:     "depot_pull_xyz",
		RegistryHost: "5m6p3sp5rj.registry.depot.dev",
		SaveForLoad:  true,
	}

	p := buildPullOpt(msg, nil, "", "auto")
	if p.imageName != "5m6p3sp5rj.registry.depot.dev/855xt0289s:16ae9e5" {
		t.Errorf("unexpected image name: %s", p.imageName)
	}
	assertServerAddress(t, p.pullOptions, "5m6p3sp5rj.registry.depot.dev")
}

func TestBuildPullOptKeepsSharedHostWithoutRegistryHost(t *testing.T) {
	msg := &cliv1.GetPullInfoResponse{
		Reference: "registry.depot.dev/855xt0289s:16ae9e5",
		Username:  "x-token",
		Password:  "depot_pull_xyz",
	}

	p := buildPullOpt(msg, nil, "", "auto")
	if p.imageName != "registry.depot.dev/855xt0289s:16ae9e5" {
		t.Errorf("unexpected image name: %s", p.imageName)
	}
	assertServerAddress(t, p.pullOptions, "registry.depot.dev")
}

func TestBakePullOptsUseOrgRegistryHost(t *testing.T) {
	app, db := "app", "db"
	msg := &cliv1.GetPullInfoResponse{
		Reference:    "registry.depot.dev/855xt0289s:16ae9e5",
		Username:     "x-token",
		Password:     "depot_pull_xyz",
		RegistryHost: "5m6p3sp5rj.registry.depot.dev",
		Options: []*cliv1.BuildOptions{
			{TargetName: &app, Command: cliv1.Command_COMMAND_BAKE, Save: true},
			{TargetName: &db, Command: cliv1.Command_COMMAND_BAKE, Save: true},
		},
	}

	pulls := bakePullOpts(msg, nil, nil, "", "auto")
	if len(pulls) != 2 {
		t.Fatalf("expected two pulls, got %d", len(pulls))
	}
	want := []string{
		"5m6p3sp5rj.registry.depot.dev/855xt0289s:16ae9e5-app",
		"5m6p3sp5rj.registry.depot.dev/855xt0289s:16ae9e5-db",
	}
	for i, p := range pulls {
		if p.imageName != want[i] {
			t.Errorf("unexpected image name: %s, want %s", p.imageName, want[i])
		}
		assertServerAddress(t, p.pullOptions, "5m6p3sp5rj.registry.depot.dev")
	}
}

func assertServerAddress(t *testing.T, opts load.PullOptions, want string) {
	t.Helper()
	if opts.ServerAddress == nil {
		t.Fatal("expected a registry server address")
	}
	if *opts.ServerAddress != want {
		t.Errorf("unexpected server address: %s, want %s", *opts.ServerAddress, want)
	}
}
