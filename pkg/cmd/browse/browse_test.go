package browse

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestResolveDestination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		location string
		orgID    string
		want     string
	}{
		{name: "organization homepage", orgID: "org-123", want: "https://depot.dev/orgs/org-123/"},
		{name: "Depot CI", location: "workflows", orgID: "org-123", want: "https://depot.dev/orgs/org-123/workflows"},
		{name: "container builds alias", location: "builds", orgID: "org-123", want: "https://depot.dev/orgs/org-123/projects"},
		{name: "GitHub Actions job", location: "github-actions/jobs/87413161724", orgID: "org-123", want: "https://depot.dev/orgs/org-123/github-actions/jobs/87413161724"},
		{name: "encoded registry repository", location: "registry/repositories/depot%2Fsnapshots%2Fe2e-base/manifests", orgID: "org-123", want: "https://depot.dev/orgs/org-123/registry/repositories/depot%2Fsnapshots%2Fe2e-base/manifests"},
		{name: "query and fragment", location: "usage/2026/07?section=github-actions#details", orgID: "org-123", want: "https://depot.dev/orgs/org-123/usage/2026/07?section=github-actions#details"},
		{name: "complete Depot URL", location: "https://depot.dev/orgs/another-org/workflows?status=failed", want: "https://depot.dev/orgs/another-org/workflows?status=failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveDestination(context.Background(), tt.location, tt.orgID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("resolveDestination() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveDestinationBuildShorthand(t *testing.T) {
	t.Parallel()

	var gotBuildID string
	lookup := func(_ context.Context, buildID string) (string, error) {
		gotBuildID = buildID
		return "https://depot.dev/orgs/org-123/projects/project-123/builds/build-123", nil
	}

	got, err := resolveDestination(context.Background(), "builds/build-123?tab=logs#step", "org-123", lookup)
	if err != nil {
		t.Fatal(err)
	}
	if gotBuildID != "build-123" {
		t.Fatalf("lookup build ID = %q, want build-123", gotBuildID)
	}
	want := "https://depot.dev/orgs/org-123/projects/project-123/builds/build-123?tab=logs#step"
	if got != want {
		t.Fatalf("resolveDestination() = %q, want %q", got, want)
	}
}

func TestResolveDestinationRejectsUnsafeBuildURL(t *testing.T) {
	t.Parallel()

	lookup := func(context.Context, string) (string, error) {
		return "https://example.com/phishing", nil
	}

	_, err := resolveDestination(context.Background(), "builds/build-123", "org-123", lookup)
	if err == nil || !strings.Contains(err.Error(), "outside https://depot.dev") {
		t.Fatalf("error = %v, want unsafe build URL error", err)
	}
}

func TestResolveDestinationRejectsUnsafeOrIncompleteInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		location string
		orgID    string
		wantErr  string
	}{
		{name: "external URL", location: "https://example.com/path", wantErr: "only https://depot.dev URLs"},
		{name: "HTTP Depot URL", location: "http://depot.dev/orgs/org-123", wantErr: "only https://depot.dev URLs"},
		{name: "protocol relative URL", location: "//example.com/path", orgID: "org-123", wantErr: "only https://depot.dev URLs"},
		{name: "parent traversal", location: "../settings", orgID: "org-123", wantErr: "must not contain . or .. segments"},
		{name: "missing organization", location: "workflows", wantErr: "no organization selected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := resolveDestination(context.Background(), tt.location, tt.orgID, nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestBrowseNoBrowserPrintsResolvedURL(t *testing.T) {
	t.Parallel()

	var opened string
	cmd := newCmdBrowse(dependencies{
		currentOrg: func() string { return "org-123" },
		openURL: func(destination string) error {
			opened = destination
			return nil
		},
	})
	cmd.SetArgs([]string{"workflows", "--no-browser"})

	var stdout strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if opened != "" {
		t.Fatalf("opened %q with --no-browser", opened)
	}
	if got, want := stdout.String(), "https://depot.dev/orgs/org-123/workflows\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestBrowseOpensResolvedURL(t *testing.T) {
	t.Parallel()

	var opened string
	cmd := newCmdBrowse(dependencies{
		currentOrg: func() string { return "org-123" },
		openURL: func(destination string) error {
			opened = destination
			return nil
		},
	})
	cmd.SetArgs([]string{"builds"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, want := opened, "https://depot.dev/orgs/org-123/projects"; got != want {
		t.Fatalf("opened = %q, want %q", got, want)
	}
}

func TestBrowseOrgFlagOverridesCurrentOrganization(t *testing.T) {
	t.Parallel()

	var opened string
	cmd := newCmdBrowse(dependencies{
		currentOrg: func() string { return "configured-org" },
		openURL: func(destination string) error {
			opened = destination
			return nil
		},
	})
	cmd.SetArgs([]string{"workflows", "--org", "requested-org"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, want := opened, "https://depot.dev/orgs/requested-org/workflows"; got != want {
		t.Fatalf("opened = %q, want %q", got, want)
	}
}

func TestBrowseReportsOpenFailure(t *testing.T) {
	t.Parallel()

	cmd := newCmdBrowse(dependencies{
		currentOrg: func() string { return "org-123" },
		openURL:    func(string) error { return errors.New("boom") },
	})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"workflows"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "failed to open") {
		t.Fatalf("error = %v, want open failure", err)
	}
}

func TestBrowseHelpHighlightsProductsAndDiscoveryCommands(t *testing.T) {
	t.Parallel()

	cmd := newCmdBrowse(dependencies{})
	help := cmd.Long + "\n" + cmd.Example

	ordered := []string{
		"workflows       Depot CI",
		"builds          Depot Container Builds (alias for projects)",
		"github-actions  Depot GitHub Action Runners",
		"<no path>       Organization homepage",
	}
	last := -1
	for _, want := range ordered {
		index := strings.Index(help, want)
		if index == -1 {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
		if index < last {
			t.Fatalf("help does not preserve product priority near %q:\n%s", want, help)
		}
		last = index
	}

	for _, want := range []string{
		"If the intended product is unclear, ask the user which destination they want.",
		"depot ci workflow list --output json",
		"depot list builds --project <project-id> --output json",
		"gh run view <run-id> --json jobs",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
}
