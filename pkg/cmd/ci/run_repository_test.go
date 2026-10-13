package ci

import (
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

// Remote URLs used by the resolver tests. Each forge uses a distinct repository
// so a result identifies which remote was selected.
const (
	depotRemoteURL  = "https://acme123.code.depot.dev/widgets"
	githubRemoteURL = "https://github.com/acme/gh-widgets.git"
	originRemoteURL = "https://origin.cursor.com/git/acme/origin-widgets.git"

	depotRepo  = "widgets"
	githubRepo = "acme/gh-widgets"
	originRepo = "acme/origin-widgets"

	explicitRepo = "explicit/repo"
)

// Resolver error messages, asserted exactly so a case cannot pass by failing
// for an unrelated reason.
const (
	errNoSupported   = "no supported repository found in git remotes; use --repo and optionally --forge (github, origin, or depot)"
	errDepotRemote   = "found a Depot Code remote; use --forge depot to run this repository"
	errMultipleForge = "multiple supported forges found in git remotes; use --forge github|origin"
	errMultipleRepo  = "multiple repositories found for the selected forge; use --repo to select one"
)

const (
	depotForge  = civ1.Forge_FORGE_DEPOT_CODE
	githubForge = civ1.Forge_FORGE_GITHUB
	originForge = civ1.Forge_FORGE_ORIGIN
)

type remote struct{ name, url string }

type resolveWant struct {
	forge civ1.Forge
	repo  string
	err   string
}

func uses(forge civ1.Forge, repo string) resolveWant { return resolveWant{forge: forge, repo: repo} }
func fails(err string) resolveWant                   { return resolveWant{err: err} }

func assertResolveRunRepository(t *testing.T, dir, repo, forge string, want resolveWant) {
	t.Helper()
	got, err := resolveRunRepository(dir, repo, forge)
	if want.err != "" {
		if err == nil || err.Error() != want.err {
			t.Fatalf("resolveRunRepository(repo=%q, forge=%q) = %#v, %v; want error %q", repo, forge, got, err, want.err)
		}
		return
	}
	if err != nil {
		t.Fatalf("resolveRunRepository(repo=%q, forge=%q) error = %v; want forge %v repo %q", repo, forge, err, want.forge, want.repo)
	}
	if got.forge != want.forge || got.repo != want.repo {
		t.Fatalf("resolveRunRepository(repo=%q, forge=%q) = forge %v repo %q; want forge %v repo %q", repo, forge, got.forge, got.repo, want.forge, want.repo)
	}
}

// TestResolveRunRepositoryMatrix covers every presence combination of a Depot
// Code, GitHub, and Origin remote against each --forge value, with and without
// --repo. No remote is named "origin", so remote-name preference never applies.
func TestResolveRunRepositoryMatrix(t *testing.T) {
	type forgeCase struct {
		forge        string
		detect, repo resolveWant
	}
	tests := []struct {
		name    string
		remotes []remote
		cases   []forgeCase
	}{
		{
			name: "none",
			cases: []forgeCase{
				{forge: "", detect: fails(errNoSupported), repo: uses(githubForge, explicitRepo)},
				{forge: "depot", detect: fails(errNoSupported), repo: uses(depotForge, explicitRepo)},
				{forge: "github", detect: fails(errNoSupported), repo: uses(githubForge, explicitRepo)},
				{forge: "origin", detect: fails(errNoSupported), repo: uses(originForge, explicitRepo)},
			},
		},
		{
			name:    "depot",
			remotes: []remote{{"depot", depotRemoteURL}},
			cases: []forgeCase{
				{forge: "", detect: uses(depotForge, depotRepo), repo: uses(depotForge, explicitRepo)},
				{forge: "depot", detect: uses(depotForge, depotRepo), repo: uses(depotForge, explicitRepo)},
				{forge: "github", detect: fails(errDepotRemote), repo: uses(githubForge, explicitRepo)},
				{forge: "origin", detect: fails(errDepotRemote), repo: uses(originForge, explicitRepo)},
			},
		},
		{
			name:    "github",
			remotes: []remote{{"gh", githubRemoteURL}},
			cases: []forgeCase{
				{forge: "", detect: uses(githubForge, githubRepo), repo: uses(githubForge, explicitRepo)},
				{forge: "depot", detect: fails(errNoSupported), repo: uses(depotForge, explicitRepo)},
				{forge: "github", detect: uses(githubForge, githubRepo), repo: uses(githubForge, explicitRepo)},
				{forge: "origin", detect: fails(errNoSupported), repo: uses(originForge, explicitRepo)},
			},
		},
		{
			name:    "origin",
			remotes: []remote{{"cursor", originRemoteURL}},
			cases: []forgeCase{
				{forge: "", detect: uses(originForge, originRepo), repo: uses(originForge, explicitRepo)},
				{forge: "depot", detect: fails(errNoSupported), repo: uses(depotForge, explicitRepo)},
				{forge: "github", detect: fails(errNoSupported), repo: uses(githubForge, explicitRepo)},
				{forge: "origin", detect: uses(originForge, originRepo), repo: uses(originForge, explicitRepo)},
			},
		},
		{
			// A checkout mirrored to Depot Code keeps resolving to GitHub.
			name:    "depot+github",
			remotes: []remote{{"depot", depotRemoteURL}, {"gh", githubRemoteURL}},
			cases: []forgeCase{
				{forge: "", detect: uses(githubForge, githubRepo), repo: uses(githubForge, explicitRepo)},
				{forge: "depot", detect: uses(depotForge, depotRepo), repo: uses(depotForge, explicitRepo)},
				{forge: "github", detect: uses(githubForge, githubRepo), repo: uses(githubForge, explicitRepo)},
				{forge: "origin", detect: fails(errDepotRemote), repo: uses(originForge, explicitRepo)},
			},
		},
		{
			// A checkout mirrored to Depot Code keeps resolving to Origin.
			name:    "depot+origin",
			remotes: []remote{{"depot", depotRemoteURL}, {"cursor", originRemoteURL}},
			cases: []forgeCase{
				{forge: "", detect: uses(originForge, originRepo), repo: uses(originForge, explicitRepo)},
				{forge: "depot", detect: uses(depotForge, depotRepo), repo: uses(depotForge, explicitRepo)},
				{forge: "github", detect: fails(errDepotRemote), repo: uses(githubForge, explicitRepo)},
				{forge: "origin", detect: uses(originForge, originRepo), repo: uses(originForge, explicitRepo)},
			},
		},
		{
			name:    "github+origin",
			remotes: []remote{{"gh", githubRemoteURL}, {"cursor", originRemoteURL}},
			cases: []forgeCase{
				{forge: "", detect: fails(errMultipleForge), repo: fails(errMultipleForge)},
				{forge: "depot", detect: fails(errNoSupported), repo: uses(depotForge, explicitRepo)},
				{forge: "github", detect: uses(githubForge, githubRepo), repo: uses(githubForge, explicitRepo)},
				{forge: "origin", detect: uses(originForge, originRepo), repo: uses(originForge, explicitRepo)},
			},
		},
		{
			name:    "depot+github+origin",
			remotes: []remote{{"depot", depotRemoteURL}, {"gh", githubRemoteURL}, {"cursor", originRemoteURL}},
			cases: []forgeCase{
				{forge: "", detect: fails(errMultipleForge), repo: fails(errMultipleForge)},
				{forge: "depot", detect: uses(depotForge, depotRepo), repo: uses(depotForge, explicitRepo)},
				{forge: "github", detect: uses(githubForge, githubRepo), repo: uses(githubForge, explicitRepo)},
				{forge: "origin", detect: uses(originForge, originRepo), repo: uses(originForge, explicitRepo)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := initRunRepositoryRemotes(t, tt.remotes...)
			for _, c := range tt.cases {
				t.Run("forge="+c.forge, func(t *testing.T) {
					t.Run("detect", func(t *testing.T) { assertResolveRunRepository(t, dir, "", c.forge, c.detect) })
					t.Run("repo", func(t *testing.T) { assertResolveRunRepository(t, dir, explicitRepo, c.forge, c.repo) })
				})
			}
		})
	}
}

// Unsupported remotes neither block Depot Code inference nor count as a forge.
func TestResolveRunRepositoryIgnoresUnsupportedRemotes(t *testing.T) {
	const gitlab = "https://gitlab.com/acme/widgets.git"
	tests := []struct {
		name    string
		remotes []remote
		repo    string
		want    resolveWant
	}{
		{name: "depot", remotes: []remote{{"origin", gitlab}, {"depot", depotRemoteURL}}, want: uses(depotForge, depotRepo)},
		{name: "depot with --repo", remotes: []remote{{"origin", gitlab}, {"depot", depotRemoteURL}}, repo: explicitRepo, want: uses(depotForge, explicitRepo)},
		{name: "github", remotes: []remote{{"origin", gitlab}, {"gh", githubRemoteURL}}, want: uses(githubForge, githubRepo)},
		{name: "unsupported only", remotes: []remote{{"origin", gitlab}}, want: fails(errNoSupported)},
		{name: "unsupported only with --repo", remotes: []remote{{"origin", gitlab}}, repo: explicitRepo, want: uses(githubForge, explicitRepo)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertResolveRunRepository(t, initRunRepositoryRemotes(t, tt.remotes...), tt.repo, "", tt.want)
		})
	}
}

// The remote name "origin" is unrelated to the Origin forge: a Depot Code
// remote named origin does not override GitHub or Origin remotes.
func TestResolveRunRepositoryDepotCodeRemoteNamedOrigin(t *testing.T) {
	tests := []struct {
		name    string
		remotes []remote
		forge   string
		want    resolveWant
	}{
		{name: "with github", remotes: []remote{{"origin", depotRemoteURL}, {"gh", githubRemoteURL}}, want: uses(githubForge, githubRepo)},
		{name: "with origin forge", remotes: []remote{{"origin", depotRemoteURL}, {"cursor", originRemoteURL}}, want: uses(originForge, originRepo)},
		{name: "with github and origin forge", remotes: []remote{{"origin", depotRemoteURL}, {"gh", githubRemoteURL}, {"cursor", originRemoteURL}}, want: fails(errMultipleForge)},
		{name: "with github and origin forge, --forge depot", remotes: []remote{{"origin", depotRemoteURL}, {"gh", githubRemoteURL}, {"cursor", originRemoteURL}}, forge: "depot", want: uses(depotForge, depotRepo)},
		{name: "with github and origin forge, --forge github", remotes: []remote{{"origin", depotRemoteURL}, {"gh", githubRemoteURL}, {"cursor", originRemoteURL}}, forge: "github", want: uses(githubForge, githubRepo)},
		{name: "with github and origin forge, --forge origin", remotes: []remote{{"origin", depotRemoteURL}, {"gh", githubRemoteURL}, {"cursor", originRemoteURL}}, forge: "origin", want: uses(originForge, originRepo)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertResolveRunRepository(t, initRunRepositoryRemotes(t, tt.remotes...), "", tt.forge, tt.want)
		})
	}
}

// With several repositories on one forge, the remote named origin wins;
// without it the CLI requires --repo. Checked with and without --forge.
func TestResolveRunRepositoryMultipleRepositoriesPerForge(t *testing.T) {
	tests := []struct {
		forgeFlag  string
		forge      civ1.Forge
		urlA, urlB string
		repoA      string
	}{
		{forgeFlag: "depot", forge: depotForge, urlA: "https://acme123.code.depot.dev/widgets", urlB: "https://acme123.code.depot.dev/gadgets", repoA: "widgets"},
		{forgeFlag: "github", forge: githubForge, urlA: "https://github.com/acme/widgets.git", urlB: "https://github.com/acme/gadgets.git", repoA: "acme/widgets"},
		{forgeFlag: "origin", forge: originForge, urlA: "https://origin.cursor.com/git/acme/widgets.git", urlB: "https://origin.cursor.com/git/acme/gadgets.git", repoA: "acme/widgets"},
	}
	for _, tt := range tests {
		for _, forge := range []string{"", tt.forgeFlag} {
			t.Run(tt.forgeFlag+"/forge="+forge, func(t *testing.T) {
				// "aaa" lists before "origin", so preference is by name, not order.
				withOrigin := initRunRepositoryRemotes(t, remote{"aaa", tt.urlB}, remote{"origin", tt.urlA})
				assertResolveRunRepository(t, withOrigin, "", forge, uses(tt.forge, tt.repoA))
				assertResolveRunRepository(t, withOrigin, explicitRepo, forge, uses(tt.forge, explicitRepo))

				withoutOrigin := initRunRepositoryRemotes(t, remote{"fork", tt.urlA}, remote{"upstream", tt.urlB})
				assertResolveRunRepository(t, withoutOrigin, "", forge, fails(errMultipleRepo))
				assertResolveRunRepository(t, withoutOrigin, explicitRepo, forge, uses(tt.forge, explicitRepo))
			})
		}
	}
}

// Several remotes pointing at the same repository count as one repository.
func TestResolveRunRepositoryDuplicateRemotes(t *testing.T) {
	tests := []struct {
		name       string
		urlA, urlB string
		want       resolveWant
	}{
		{name: "depot", urlA: "https://acme123.code.depot.dev/widgets", urlB: "https://ACME123.code.depot.dev/widgets/", want: uses(depotForge, "widgets")},
		{name: "github", urlA: "https://github.com/acme/widgets.git", urlB: "git@github.com:acme/widgets.git", want: uses(githubForge, "acme/widgets")},
		{name: "origin", urlA: "https://origin.cursor.com/git/acme/widgets.git", urlB: "git@origin.cursor.com:acme/widgets", want: uses(originForge, "acme/widgets")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := initRunRepositoryRemotes(t, remote{"fork", tt.urlA}, remote{"upstream", tt.urlB})
			assertResolveRunRepository(t, dir, "", "", tt.want)
		})
	}
}

// Invalid --forge values fail before repository selection, whatever the remotes.
func TestResolveRunRepositoryInvalidForge(t *testing.T) {
	dir := initRunRepositoryRemotes(t, remote{"depot", depotRemoteURL}, remote{"gh", githubRemoteURL})
	for _, forge := range []string{"gitlab", "GitHub", "depot-code"} {
		for _, repo := range []string{"", explicitRepo} {
			t.Run(forge+"/repo="+repo, func(t *testing.T) {
				want := `unsupported forge "` + forge + `"; expected github, origin, or depot`
				assertResolveRunRepository(t, dir, repo, forge, fails(want))
			})
		}
	}
}

func initRunRepositoryRemotes(t *testing.T, remotes ...remote) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init")
	for _, r := range remotes {
		run(t, dir, "git", "remote", "add", r.name, r.url)
	}
	return dir
}
