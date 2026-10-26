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
		{remoteURL: "entire://aws-us-east-2.entire.io/et/acme/api", forge: civ1.Forge_FORGE_ENTIRE, repo: "et/acme/api", ok: true},
		{remoteURL: "https://aws-eu-central-1.entire.io/et/acme/api.git", forge: civ1.Forge_FORGE_ENTIRE, repo: "et/acme/api", ok: true},
		{remoteURL: "entire://aws-us-east-2.entire.io/gh/acme/api", forge: civ1.Forge_FORGE_ENTIRE, repo: "gh/acme/api", ok: true},
		{remoteURL: "https://aws-us-east-2.entire.io/acme/api.git", ok: false},
		{remoteURL: "entire://aws-us-east-2.entire.io.evil.com/et/acme/api", ok: false},
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

// Remotes for the resolver tests, keyed by --forge value. Each forge uses a
// distinct repository so a result identifies the selected remote.
var testRemotes = map[string]struct {
	url  string
	want runRepository
}{
	"depot":  {"https://acme123.code.depot.dev/widgets", runRepository{civ1.Forge_FORGE_DEPOT_CODE, "widgets"}},
	"github": {"https://github.com/acme/gh-widgets.git", runRepository{civ1.Forge_FORGE_GITHUB, "acme/gh-widgets"}},
	"origin": {"https://origin.cursor.com/git/acme/origin-widgets.git", runRepository{civ1.Forge_FORGE_ORIGIN, "acme/origin-widgets"}},
}

const (
	errNoRepo        = "no supported repository found in git remotes; use --repo and optionally --forge (github, origin, depot, or entire)"
	errDepotRemote   = "found a Depot Code remote; use --forge depot to run this repository"
	errMultipleForge = "multiple supported forges found in git remotes; use --forge github|origin|entire"
)

// TestResolveRunRepositoryMatrix covers each combination of Depot Code, GitHub,
// and Origin remotes against every --forge value, with and without --repo.
// Each entry is the forge the run resolves to, or the exact error. No remote is
// named origin, so remote-name preference never applies.
func TestResolveRunRepositoryMatrix(t *testing.T) {
	forges := [4]string{"", "depot", "github", "origin"}
	// Columns follow forges.
	detect := map[string][4]string{
		"none":   {errNoRepo, errNoRepo, errNoRepo, errNoRepo},
		"depot":  {"depot", "depot", errDepotRemote, errDepotRemote},
		"github": {"github", errNoRepo, "github", errNoRepo},
		"origin": {"origin", errNoRepo, errNoRepo, "origin"},
		// Mirrors keep resolving to GitHub or Origin unless --forge depot is passed.
		"depot+github":        {"github", "depot", "github", errDepotRemote},
		"depot+origin":        {"origin", "depot", errDepotRemote, "origin"},
		"github+origin":       {errMultipleForge, errNoRepo, "github", "origin"},
		"depot+github+origin": {errMultipleForge, "depot", "github", "origin"},
	}
	withRepo := map[string][4]string{
		"none":                {"github", "depot", "github", "origin"},
		"depot":               {"depot", "depot", "github", "origin"},
		"github":              {"github", "depot", "github", "origin"},
		"origin":              {"origin", "depot", "github", "origin"},
		"depot+github":        {"github", "depot", "github", "origin"},
		"depot+origin":        {"origin", "depot", "github", "origin"},
		"github+origin":       {errMultipleForge, "depot", "github", "origin"},
		"depot+github+origin": {errMultipleForge, "depot", "github", "origin"},
	}
	expected := func(entry, repo string) (runRepository, string) {
		r, ok := testRemotes[entry]
		if !ok {
			return runRepository{}, entry
		}
		if repo != "" {
			r.want.repo = repo
		}
		return r.want, ""
	}

	for combination := range detect {
		var remotes []remote
		if combination != "none" {
			for _, key := range strings.Split(combination, "+") {
				remotes = append(remotes, remote{key + "-remote", testRemotes[key].url})
			}
		}
		dir := initRunRepositoryRemotes(t, remotes...)
		for i, forge := range forges {
			t.Run(combination+"/forge="+forge, func(t *testing.T) {
				want, wantErr := expected(detect[combination][i], "")
				assertResolveRunRepository(t, dir, "", forge, want, wantErr)
				want, wantErr = expected(withRepo[combination][i], "explicit/repo")
				assertResolveRunRepository(t, dir, "explicit/repo", forge, want, wantErr)
			})
		}
	}
}

// Without --forge, unsupported remotes and remote names do not change which
// forge is inferred, but the remote named origin picks between repositories
// on one forge.
func TestResolveRunRepositoryRemotes(t *testing.T) {
	depot, github, origin := testRemotes["depot"], testRemotes["github"], testRemotes["origin"]
	tests := []struct {
		name    string
		remotes []remote
		want    runRepository
		wantErr string
	}{
		{
			name:    "unsupported remote does not block Depot Code inference",
			remotes: []remote{{"origin", "https://gitlab.com/acme/widgets.git"}, {"depot", depot.url}},
			want:    depot.want,
		},
		{
			name:    "Depot Code remote named origin in a GitHub mirror",
			remotes: []remote{{"origin", depot.url}, {"gh", github.url}},
			want:    github.want,
		},
		{
			name:    "Depot Code remote named origin in an Origin mirror",
			remotes: []remote{{"origin", depot.url}, {"cursor", origin.url}},
			want:    origin.want,
		},
		// "aaa" lists before "origin", so preference is by name, not order.
		{
			name:    "multiple Depot Code repositories prefer origin",
			remotes: []remote{{"aaa", "https://acme123.code.depot.dev/gadgets"}, {"origin", depot.url}},
			want:    depot.want,
		},
		{
			name:    "multiple Depot Code repositories without origin",
			remotes: []remote{{"fork", depot.url}, {"upstream", "https://acme123.code.depot.dev/gadgets"}},
			wantErr: "multiple repositories found for the selected forge; use --repo to select one",
		},
		{
			name:    "multiple GitHub repositories prefer origin",
			remotes: []remote{{"aaa", "https://github.com/acme/gadgets.git"}, {"origin", github.url}},
			want:    github.want,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertResolveRunRepository(t, initRunRepositoryRemotes(t, tt.remotes...), "", "", tt.want, tt.wantErr)
		})
	}
}

type remote struct{ name, url string }

func initRunRepositoryRemotes(t *testing.T, remotes ...remote) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init")
	for _, r := range remotes {
		run(t, dir, "git", "remote", "add", r.name, r.url)
	}
	return dir
}

// assertResolveRunRepository checks errors exactly so a case cannot pass by
// failing for an unrelated reason.
func assertResolveRunRepository(t *testing.T, dir, repo, forge string, want runRepository, wantErr string) {
	t.Helper()
	got, err := resolveRunRepository(dir, repo, forge)
	if wantErr != "" {
		if err == nil || err.Error() != wantErr {
			t.Fatalf("resolveRunRepository(repo=%q, forge=%q) = %#v, %v; want error %q", repo, forge, got, err, wantErr)
		}
		return
	}
	if err != nil || got != want {
		t.Fatalf("resolveRunRepository(repo=%q, forge=%q) = %#v, %v; want %#v", repo, forge, got, err, want)
	}
}
