package browse

import (
	"context"
	"errors"
	"strings"
	"testing"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
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
		{name: "container builds alias with trailing slash", location: "builds/", orgID: "org-123", want: "https://depot.dev/orgs/org-123/projects"},
		{name: "GitHub Actions job", location: "github-actions/jobs/87413161724", orgID: "org-123", want: "https://depot.dev/orgs/org-123/github-actions/jobs/87413161724"},
		{name: "encoded registry repository", location: "registry/repositories/depot%2Fsnapshots%2Fe2e-base/manifests", orgID: "org-123", want: "https://depot.dev/orgs/org-123/registry/repositories/depot%2Fsnapshots%2Fe2e-base/manifests"},
		{name: "query and fragment", location: "usage/2026/07?section=github-actions#details", orgID: "org-123", want: "https://depot.dev/orgs/org-123/usage/2026/07?section=github-actions#details"},
		{name: "complete Depot URL", location: "https://depot.dev/orgs/another-org/workflows?status=failed", want: "https://depot.dev/orgs/another-org/workflows?status=failed"},
		{name: "complete Depot URL with mixed-case host", location: "https://Depot.Dev/orgs/another-org/workflows", want: "https://Depot.Dev/orgs/another-org/workflows"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveDestination(context.Background(), tt.location, tt.orgID, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("resolveDestination() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveDestinationBuildShorthandDoesNotRequireOrganization(t *testing.T) {
	t.Parallel()

	lookup := func(context.Context, string) (string, error) {
		return "https://depot.dev/orgs/org-123/projects/project-123/builds/build-123", nil
	}

	got, err := resolveDestination(context.Background(), "builds/build-123", "", lookup, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://depot.dev/orgs/org-123/projects/project-123/builds/build-123"; got != want {
		t.Fatalf("resolveDestination() = %q, want %q", got, want)
	}
}

func TestResolveDestinationBuildShorthand(t *testing.T) {
	t.Parallel()

	var gotBuildID string
	lookup := func(_ context.Context, buildID string) (string, error) {
		gotBuildID = buildID
		return "https://depot.dev/orgs/org-123/projects/project-123/builds/build-123", nil
	}

	got, err := resolveDestination(context.Background(), "builds/build-123?tab=logs#step", "org-123", lookup, nil)
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

	_, err := resolveDestination(context.Background(), "builds/build-123", "org-123", lookup, nil)
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

			_, err := resolveDestination(context.Background(), tt.location, tt.orgID, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestResolveDestinationLooksUpBareID(t *testing.T) {
	t.Parallel()

	lookup := func(_ context.Context, id, orgID string) ([]entityDestination, error) {
		if id != "workflow-123" || orgID != "org-123" {
			t.Fatalf("lookup(%q, %q), want workflow-123, org-123", id, orgID)
		}
		return []entityDestination{{
			kind: "Depot CI workflow",
			path: "workflows/workflow-123",
			url:  "https://depot.dev/orgs/org-123/workflows/workflow-123",
		}}, nil
	}

	got, err := resolveDestination(context.Background(), "workflow-123?view=graph#failed", "org-123", nil, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://depot.dev/orgs/org-123/workflows/workflow-123?view=graph#failed"; got != want {
		t.Fatalf("resolveDestination() = %q, want %q", got, want)
	}
}

func TestResolveDestinationDoesNotLookUpKnownPath(t *testing.T) {
	t.Parallel()

	lookup := func(context.Context, string, string) ([]entityDestination, error) {
		t.Fatal("lookup called for known app path")
		return nil, nil
	}

	got, err := resolveDestination(context.Background(), "registry", "org-123", nil, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://depot.dev/orgs/org-123/registry"; got != want {
		t.Fatalf("resolveDestination() = %q, want %q", got, want)
	}
}

func TestResolveDestinationRejectsAmbiguousBareID(t *testing.T) {
	t.Parallel()

	lookup := func(context.Context, string, string) ([]entityDestination, error) {
		return []entityDestination{
			{kind: "build", path: "builds/shared-id", url: "https://depot.dev/orgs/org-123/projects/project-1/builds/shared-id"},
			{kind: "Depot CI workflow", path: "workflows/shared-id", url: "https://depot.dev/orgs/org-123/workflows/shared-id"},
		}, nil
	}

	_, err := resolveDestination(context.Background(), "shared-id", "org-123", nil, lookup)
	if err == nil || !strings.Contains(err.Error(), "shared-id is ambiguous") || !strings.Contains(err.Error(), "builds/shared-id") || !strings.Contains(err.Error(), "workflows/shared-id") {
		t.Fatalf("error = %v, want ambiguity with explicit paths", err)
	}
}

func TestResolveDestinationReportsUnknownBareID(t *testing.T) {
	t.Parallel()

	lookup := func(context.Context, string, string) ([]entityDestination, error) { return nil, nil }
	_, err := resolveDestination(context.Background(), "missing-id", "org-123", nil, lookup)
	if err == nil || !strings.Contains(err.Error(), "could not find missing-id") || !strings.Contains(err.Error(), "use an explicit path") {
		t.Fatalf("error = %v, want not-found guidance", err)
	}
}

func TestLookupEntityRecognizesGitHubActionsJobID(t *testing.T) {
	t.Parallel()

	matches, err := lookupEntity(context.Background(), "87413161724", "org-123")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %v, want one", matches)
	}
	if got, want := matches[0].url, "https://depot.dev/orgs/org-123/github-actions/jobs/87413161724"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

func TestLookupCIJobRecoversWorkflowFromMetrics(t *testing.T) {
	t.Parallel()

	destination, err := lookupCIJob(
		context.Background(),
		"token-123",
		"org-123",
		"job-123",
		func(context.Context, string, string, *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error) {
			return &civ1.GetJobSummaryResponse{JobId: "job-123", JobStatus: "queued", EmptyReason: "no_attempt"}, nil
		},
		func(context.Context, string, string, string) (*civ1.GetJobMetricsResponse, error) {
			return &civ1.GetJobMetricsResponse{
				Workflow: &civ1.CIMetricsWorkflowContext{WorkflowId: "workflow-123"},
				Job:      &civ1.CIMetricsJobContext{JobId: "job-123"},
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := destination.url, "https://depot.dev/orgs/org-123/workflows/workflow-123/jobs/job-123"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

func TestFinishEntityLookupKeepsConfirmedMatches(t *testing.T) {
	t.Parallel()

	matches := []entityDestination{{kind: "build", path: "builds/build-123", url: "https://depot.dev/build-123"}}
	got, err := finishEntityLookup(matches, []string{"Depot CI workflow lookup failed: unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != matches[0] {
		t.Fatalf("matches = %v, want %v", got, matches)
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
		"single-segment path is looked up as a build, Depot CI workflow or job",
		"depot ci workflow list --output json",
		"depot ci workflow show <workflow-id> --output json",
		"depot list builds --project <project-id> --output json",
		"Find job IDs with: depot browse github-actions",
		"depot browse <id>",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
}
