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
		{remoteURL: "https://origin.cursor.com/git/owner/repo.git", forge: civ1.Forge_FORGE_CURSOR_ORIGIN, repo: "owner/repo", ok: true},
		{remoteURL: "https://origin.cursor.com/owner/repo", forge: civ1.Forge_FORGE_CURSOR_ORIGIN, repo: "owner/repo", ok: true},
		{remoteURL: "git@origin.cursor.com:owner/repo", forge: civ1.Forge_FORGE_CURSOR_ORIGIN, repo: "owner/repo", ok: true},
		{remoteURL: "https://x-token@org.code.depot.dev/canonical-name.git", forge: civ1.Forge_FORGE_DEPOT_CODE, repo: "canonical-name", ok: true},
		{remoteURL: "git@org.code.preview.depot.dev:team/canonical-name.git", forge: civ1.Forge_FORGE_DEPOT_CODE, repo: "team/canonical-name", ok: true},
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

func TestResolveRunRepositorySingleCursorOrigin(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://origin.cursor.com/git/acme/widgets.git")
	got, err := resolveRunRepository(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_CURSOR_ORIGIN || got.repo != "acme/widgets" {
		t.Fatalf("resolveRunRepository() = %#v", got)
	}
}

func TestResolveRunRepositorySingleDepotCode(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://token@acme.code.depot.dev/widgets.git")
	got, err := resolveRunRepository(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_DEPOT_CODE || got.repo != "widgets" {
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
	run(t, dir, "git", "remote", "add", "cursor", "git@origin.cursor.com:acme/widgets.git")

	_, err := resolveRunRepository(dir, "", "")
	if err == nil || !strings.Contains(err.Error(), "--forge") {
		t.Fatalf("resolveRunRepository() error = %v, want --forge guidance", err)
	}
	_, err = resolveRunRepository(dir, "explicit/repo", "")
	if err == nil || !strings.Contains(err.Error(), "--forge") {
		t.Fatalf("resolveRunRepository() with --repo error = %v, want --forge guidance", err)
	}

	got, err := resolveRunRepository(dir, "", "cursor-origin")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_CURSOR_ORIGIN || got.repo != "acme/widgets" {
		t.Fatalf("resolveRunRepository() with --forge = %#v", got)
	}
}

func TestResolveRunRepositoryExplicitRepoAndForge(t *testing.T) {
	dir := initRunRepositoryGit(t, "https://gitlab.com/acme/widgets.git")
	got, err := resolveRunRepository(dir, "acme/widgets", "cursor-origin")
	if err != nil {
		t.Fatal(err)
	}
	if got.forge != civ1.Forge_FORGE_CURSOR_ORIGIN || got.repo != "acme/widgets" {
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
