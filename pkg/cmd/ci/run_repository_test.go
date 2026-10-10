package ci

import (
	"strings"
	"testing"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func TestParseRunRepository(t *testing.T) {
	tests := []struct {
		remoteURL string
		forge     civ1.Forge
		repo      string
		ok        bool
	}{
		{remoteURL: "https://user:token@github.com/owner/repo.git", forge: civ1.Forge_FORGE_GITHUB, repo: "owner/repo", ok: true},
		{remoteURL: "git@github.com:owner/repo.git", forge: civ1.Forge_FORGE_GITHUB, repo: "owner/repo", ok: true},
		{remoteURL: "https://origin.cursor.com/git/owner/repo.git", forge: civ1.Forge_FORGE_ORIGIN, repo: "owner/repo", ok: true},
		{remoteURL: "https://origin.cursor.com/owner/repo", forge: civ1.Forge_FORGE_ORIGIN, repo: "owner/repo", ok: true},
		{remoteURL: "git@origin.cursor.com:owner/repo", forge: civ1.Forge_FORGE_ORIGIN, repo: "owner/repo", ok: true},
		{remoteURL: "https://acme123.code.depot.dev/widgets", forge: civ1.Forge_FORGE_DEPOT_CODE, repo: "widgets", ok: true},
		{remoteURL: "https://ACME123.code.depot.dev/widgets/", forge: civ1.Forge_FORGE_DEPOT_CODE, repo: "widgets", ok: true},
		{remoteURL: "https://code.depot.dev/team/widgets", forge: civ1.Forge_FORGE_DEPOT_CODE, repo: "team/widgets", ok: true},
		{remoteURL: "https://acme123.code.depot.dev/widgets.git", forge: civ1.Forge_FORGE_DEPOT_CODE, repo: "widgets.git", ok: true},
		{remoteURL: "https://acme123.code.depot.dev/", ok: false},
		{remoteURL: "https://notcode.depot.dev/widgets", ok: false},
		{remoteURL: "https://gitlab.com/owner/repo.git", ok: false},
		{remoteURL: "https://github.com/owner/repo/extra", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.remoteURL, func(t *testing.T) {
			got, ok := parseRunRepository(tt.remoteURL)
			if ok != tt.ok {
				t.Fatalf("parseRunRepository() ok = %v, want %v", ok, tt.ok)
			}
			if ok && (got.forge != tt.forge || got.repo != tt.repo) {
				t.Fatalf("parseRunRepository() = %#v, want forge %v repo %q", got, tt.forge, tt.repo)
			}
		})
	}
}

func TestResolveRunRepositorySingleOrigin(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://origin.cursor.com/git/acme/widgets.git")
	got, err := resolveRunRepository(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_ORIGIN || got.repo != "acme/widgets" {
		t.Fatalf("resolveRunRepository() = %#v", got)
	}
}

func TestResolveRunRepositoryPrefersOriginForSameForge(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://github.com/acme/widgets-fork.git")
	run(t, dir, "git", "remote", "add", "upstream", "https://github.com/acme/widgets.git")

	got, err := resolveRunRepository(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_GITHUB || got.repo != "acme/widgets-fork" {
		t.Fatalf("resolveRunRepository() = %#v", got)
	}
}

func TestResolveRunRepositoryExplicitRepoDefaultsToGitHub(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://gitlab.com/acme/widgets.git")
	got, err := resolveRunRepository(dir, "other/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_GITHUB || got.repo != "other/repo" {
		t.Fatalf("resolveRunRepository() = %#v", got)
	}
}

func TestResolveRunRepositoryAmbiguousForges(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://github.com/acme/widgets.git")
	run(t, dir, "git", "remote", "add", "origin-forge", "git@origin.cursor.com:acme/widgets.git")

	_, err := resolveRunRepository(dir, "", "")
	if err == nil || !strings.Contains(err.Error(), "--forge") {
		t.Fatalf("resolveRunRepository() error = %v, want --forge guidance", err)
	}
	_, err = resolveRunRepository(dir, "explicit/repo", "")
	if err == nil || !strings.Contains(err.Error(), "--forge") {
		t.Fatalf("resolveRunRepository() with --repo error = %v, want --forge guidance", err)
	}

	got, err := resolveRunRepository(dir, "", "origin")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_ORIGIN || got.repo != "acme/widgets" {
		t.Fatalf("resolveRunRepository() with --forge = %#v", got)
	}
}

func TestResolveRunRepositoryExplicitRepoAndForge(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://gitlab.com/acme/widgets.git")
	got, err := resolveRunRepository(dir, "acme/widgets", "origin")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_ORIGIN || got.repo != "acme/widgets" {
		t.Fatalf("resolveRunRepository() = %#v", got)
	}
}

func TestResolveRunRepositoryDepotCodeWithForge(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://acme123.code.depot.dev/widgets")
	got, err := resolveRunRepository(dir, "", "depot")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_DEPOT_CODE || got.repo != "widgets" {
		t.Fatalf("resolveRunRepository() = %#v", got)
	}
}

func TestResolveRunRepositoryDepotCodeRequiresForge(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://acme123.code.depot.dev/widgets")
	_, err := resolveRunRepository(dir, "", "")
	if err == nil || !strings.Contains(err.Error(), "--forge depot") {
		t.Fatalf("resolveRunRepository() error = %v, want --forge depot guidance", err)
	}
}

func TestResolveRunRepositoryDepotCodeExplicitRepo(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://gitlab.com/acme/widgets.git")
	got, err := resolveRunRepository(dir, "widgets", "depot")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_DEPOT_CODE || got.repo != "widgets" {
		t.Fatalf("resolveRunRepository() = %#v", got)
	}
}

// A checkout that mirrors a GitHub repository has both remotes. It must keep
// resolving to GitHub unless --forge depot is passed.
func TestResolveRunRepositoryMirrorCheckoutStaysOnGitHub(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://github.com/acme/widgets.git")
	run(t, dir, "git", "remote", "add", "depot", "https://acme123.code.depot.dev/acme/widgets")

	got, err := resolveRunRepository(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_GITHUB || got.repo != "acme/widgets" {
		t.Fatalf("resolveRunRepository() = %#v", got)
	}

	got, err = resolveRunRepository(dir, "other/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_GITHUB || got.repo != "other/repo" {
		t.Fatalf("resolveRunRepository() with --repo = %#v", got)
	}

	got, err = resolveRunRepository(dir, "", "depot")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_DEPOT_CODE || got.repo != "acme/widgets" {
		t.Fatalf("resolveRunRepository() with --forge depot = %#v", got)
	}
}

func TestResolveRunRepositoryDepotCodeOriginRemoteIgnoredWithoutForge(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://acme123.code.depot.dev/widgets")
	run(t, dir, "git", "remote", "add", "github", "https://github.com/acme/widgets.git")

	got, err := resolveRunRepository(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_GITHUB || got.repo != "acme/widgets" {
		t.Fatalf("resolveRunRepository() = %#v", got)
	}
}

func initRunRepositoryGit(t *testing.T, remoteURL string) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init")
	run(t, dir, "git", "remote", "add", "origin", remoteURL)
	return dir
}
