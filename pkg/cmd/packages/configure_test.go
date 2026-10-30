package packages

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestSetup(t *testing.T, format, name string) *setup {
	t.Helper()
	t.Setenv("DEPOT_PACKAGES_URL", "")
	repo, err := newRepository("org123", format, name)
	require.NoError(t, err)
	return &setup{ctx: context.Background(), repo: repo, token: "tok_secret", store: true}
}

func TestNewRepositoryValidatesNames(t *testing.T) {
	t.Setenv("DEPOT_PACKAGES_URL", "")

	repo, err := newRepository("org123", "npm", "team-a")
	require.NoError(t, err)
	require.Equal(t, "https://packages.depot.dev/org123/npm/team-a/", repo.url())
	require.Equal(t, "//packages.depot.dev/org123/npm/team-a/", repo.nerfDart())

	for _, name := range []string{"", "Team", "-team", "team-", "te--am", "team/x", strings.Repeat("a", 65)} {
		_, err := newRepository("org123", "npm", name)
		require.Error(t, err, name)
	}
	_, err = newRepository("org/123", "npm", "team")
	require.Error(t, err)
}

func TestWriteNpmrcPreservesUnrelatedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	t.Setenv("NPM_CONFIG_USERCONFIG", path)
	require.NoError(t, os.WriteFile(path, []byte("; comment\nsave-exact=true\nregistry=https://registry.npmjs.org/\n//packages.depot.dev/org123/npm/npmjs/:tokenHelper=/old\n"), 0644))

	s := newTestSetup(t, "npm", "npmjs")
	require.NoError(t, writeNpmrc(s, ""))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "; comment\nsave-exact=true\nregistry=https://packages.depot.dev/org123/npm/npmjs/\n//packages.depot.dev/org123/npm/npmjs/:_authToken=tok_secret\n", string(data))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestWriteNpmrcScopeAndHelper(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	t.Setenv("NPM_CONFIG_USERCONFIG", path)
	require.NoError(t, os.WriteFile(path, []byte("registry=https://registry.npmjs.org/\n//packages.depot.dev/org123/npm/internal/:_authToken=old\n"), 0600))

	s := newTestSetup(t, "npm", "internal")
	s.opts.scope = "@acme"
	require.NoError(t, writeNpmrc(s, "/home/me/.depot/bin/depot-package-token"))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "registry=https://registry.npmjs.org/\n@acme:registry=https://packages.depot.dev/org123/npm/internal/\n//packages.depot.dev/org123/npm/internal/:tokenHelper=/home/me/.depot/bin/depot-package-token\n", string(data))
}

func TestWriteCargoConfigReplacesManagedTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "# keep\n[net]\ngit-fetch-with-cli = true\n\n[registries.depot]\nindex = \"sparse+https://packages.depot.dev/org123/cargo/old/index/\"\n\n[source.crates-io]\nreplace-with = \"depot\"\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0644))

	s := newTestSetup(t, "cargo", "internal")
	require.NoError(t, writeCargoConfig(s, path, []string{"cargo:token"}))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "# keep\n[net]\ngit-fetch-with-cli = true\n\n[registries.depot]\nindex = \"sparse+https://packages.depot.dev/org123/cargo/internal/index/\"\ncredential-provider = [\"cargo:token\"]\n", string(data))

	s.replaceUpstream = true
	require.NoError(t, writeCargoConfig(s, path, []string{"cargo:token"}))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(string(data), "\n\n[source.crates-io]\nreplace-with = \"depot\"\n"))
}

func TestWriteCargoConfigRejectsOtherReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[source.crates-io]\nreplace-with = \"vendored\"\n"), 0644))

	s := newTestSetup(t, "cargo", "crates-io")
	s.replaceUpstream = true
	require.ErrorContains(t, writeCargoConfig(s, path, []string{"cargo:token"}), `replaces crates.io with "vendored"`)
}

func TestWriteCargoConfigRejectsInlineRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[registries]\ndepot = { index = \"sparse+https://packages.depot.dev/org123/cargo/old/index/\" }\n"), 0644))

	s := newTestSetup(t, "cargo", "internal")
	require.ErrorContains(t, writeCargoConfig(s, path, []string{"cargo:token"}), "can't update")
}

func TestSetNetrcMachine(t *testing.T) {
	existing := "machine example.com\n  login a\n  # comment\n  password b\nmachine packages.depot.dev login depot password old\ndefault login anon password x\n"
	out, err := setNetrcMachine([]byte(existing), "packages.depot.dev", "depot", "tok_new")
	require.NoError(t, err)
	require.Equal(t, "machine example.com\n  login a\n  # comment\n  password b\nmachine packages.depot.dev login depot password tok_new\ndefault login anon password x\n", string(out))

	out, err = setNetrcMachine([]byte("machine packages.depot.dev\nlogin depot\npassword old"), "packages.depot.dev", "depot", "tok_new")
	require.NoError(t, err)
	require.Equal(t, "machine packages.depot.dev login depot password tok_new\n", string(out))

	_, err = setNetrcMachine([]byte("macdef init\n"), "packages.depot.dev", "depot", "tok")
	require.Error(t, err)
}

func TestWriteMambarc(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mambarc")
	require.NoError(t, os.WriteFile(path, []byte("# settings\nchannels:\n  - conda-forge\n  - bioconda\nalways_yes: true\n"), 0644))

	s := newTestSetup(t, "conda", "conda-forge")
	s.replaceUpstream = true
	require.NoError(t, writeMambarc(s, path, isCondaUpstream))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "# settings\nchannels:\n  - https://packages.depot.dev/org123/conda/conda-forge\n  - bioconda\nalways_yes: true\ndefault_channels: []\n", string(data))
}

func TestWriteGemCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	require.NoError(t, os.WriteFile(path, []byte("---\n:rubygems_api_key: abc\n:depot: old\n"), 0644))

	s := newTestSetup(t, "rubygems", "internal")
	require.NoError(t, writeGemCredential(s, path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "---\n:rubygems_api_key: abc\n:depot: old\n:depot-org123_internal: tok_secret\n", string(data))
}

func TestAlpineBranch(t *testing.T) {
	stable := "https://dl-cdn.alpinelinux.org/alpine/v3.22/main\nhttps://dl-cdn.alpinelinux.org/alpine/v3.22/community\n"
	edge := "# mirrors\n@testing https://dl-cdn.alpinelinux.org/alpine/edge/testing\nhttps://dl-cdn.alpinelinux.org/alpine/edge/main\n"

	require.Equal(t, "v3.22", alpineBranch(stable, "3.22.1\n"))
	require.Equal(t, "edge", alpineBranch(edge, "3.23.0_alpha20251016\n"))
	require.Equal(t, "edge", alpineBranch("", "3.23.0_alpha20251016\n"))
	require.Equal(t, "v3.21", alpineBranch("", "3.21.4\n"))
	require.Equal(t, "", alpineBranch("", ""))
}

func TestDefaultAptSuite(t *testing.T) {
	require.Equal(t, "bookworm", defaultAptSuite("debian", "bookworm"))
	require.Equal(t, "bookworm-security", defaultAptSuite("debian-security", "bookworm"))
	require.Equal(t, "", defaultAptSuite("debian-security", ""))
}

func TestMambaVersionPattern(t *testing.T) {
	require.Equal(t, "1", mambaVersionPattern.FindStringSubmatch("mamba 1.4.2\nconda 23.3.1\n")[1])
	require.Equal(t, "2", mambaVersionPattern.FindStringSubmatch("2.9.0\n")[1])
}

func TestRubyConfigPathOverrides(t *testing.T) {
	t.Setenv("BUNDLE_CONFIG", "/tmp/bundle-config")
	t.Setenv("BUNDLE_USER_CONFIG", "/tmp/bundle-user-config")
	path, err := bundlerConfigPath()
	require.NoError(t, err)
	require.Equal(t, "/tmp/bundle-config", path)

	t.Setenv("GEMRC", "/tmp/gemrc"+string(os.PathListSeparator)+"/tmp/other")
	path, err = gemConfigPath()
	require.NoError(t, err)
	require.Equal(t, "/tmp/other", path)
}

func TestBundlerKey(t *testing.T) {
	require.Equal(t, "BUNDLE_HTTPS://PACKAGES__DEPOT__DEV/ORG123/RUBYGEMS/MY___GEMS/", bundlerKey("https://packages.depot.dev/org123/rubygems/my-gems/"))
	require.Equal(t, "BUNDLE_MIRROR__HTTPS://RUBYGEMS__ORG/", bundlerKey("mirror.https://rubygems.org/"))
}

func TestWriteGemSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gemrc")
	require.NoError(t, os.WriteFile(path, []byte("---\n:backtrace: true\n:sources:\n- \"https://rubygems.org/\"\n- \"https://depot:old@packages.depot.dev/org123/rubygems/rubygems/\"\n:verbose: true\n"), 0644))

	s := newTestSetup(t, "rubygems", "rubygems")
	s.replaceUpstream = true
	require.NoError(t, writeGemSources(s, path))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "---\n:backtrace: true\n:sources:\n- \"https://depot:tok_secret@packages.depot.dev/org123/rubygems/rubygems/\"\n:verbose: true\n", string(data))

	s.replaceUpstream = false
	s.replaceUpstreamExplicit = true
	require.NoError(t, writeGemSources(s, path))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "---\n:backtrace: true\n:sources:\n- \"https://rubygems.org/\"\n- \"https://depot:tok_secret@packages.depot.dev/org123/rubygems/rubygems/\"\n:verbose: true\n", string(data))
}

func TestWriteMambarcUndoesReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mambarc")
	require.NoError(t, os.WriteFile(path, []byte("channels:\n  - https://packages.depot.dev/org123/conda/conda-forge\ndefault_channels: []\n"), 0644))

	s := newTestSetup(t, "conda", "conda-forge")
	s.replaceUpstreamExplicit = true
	require.NoError(t, writeMambarc(s, path, isCondaUpstream))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "channels:\n  - https://packages.depot.dev/org123/conda/conda-forge\n  - https://conda.anaconda.org/conda-forge\n", string(data))
}

func TestSetNetrcMachineBeforeDefault(t *testing.T) {
	out, err := setNetrcMachine([]byte("machine example.com login a password b\ndefault login anon password x\n"), "packages.depot.dev", "depot", "tok")
	require.NoError(t, err)
	require.Equal(t, "machine example.com login a password b\nmachine packages.depot.dev login depot password tok\ndefault login anon password x\n", string(out))
}

func TestPackagesServerRejectsQuery(t *testing.T) {
	for _, raw := range []string{"https://host/base?tenant=x", "https://host/base#x", "https://user:pass@host", "ftp://host"} {
		t.Setenv("DEPOT_PACKAGES_URL", raw)
		_, err := packagesServer()
		require.Error(t, err, raw)
	}
}

func TestRubyConfigListAcrossComments(t *testing.T) {
	config := parseRubyConfig([]byte("---\n:sources:\n- \"https://a/\"\n# note\n\n- \"https://rubygems.org/\"\n:verbose: true\n"))
	items, found, err := config.list(":sources")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []string{"https://a/", "https://rubygems.org/"}, items)

	config.setList(":sources", []string{"https://a/"})
	require.Equal(t, "---\n:sources:\n- \"https://a/\"\n:verbose: true\n", string(config.bytes()))
}

func TestSetNetrcMachineIgnoresKeywordValues(t *testing.T) {
	existing := "machine example.com login default password machine\nmachine other.example login a password default\n"
	out, err := setNetrcMachine([]byte(existing), "packages.depot.dev", "depot", "tok")
	require.NoError(t, err)
	require.Equal(t, existing+"machine packages.depot.dev login depot password tok\n", string(out))
}

func TestWriteFileFollowsDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "npmrc")
	link := filepath.Join(dir, ".npmrc")
	require.NoError(t, os.Symlink(target, link))

	s := newTestSetup(t, "npm", "npmjs")
	require.NoError(t, s.writeFile(link, []byte("registry=x\n"), 0600))

	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "registry=x\n", string(data))
}

func TestCargoFilePrefersLegacyName(t *testing.T) {
	dir := t.TempDir()
	require.Equal(t, filepath.Join(dir, "config.toml"), cargoFile(dir, "config"))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), nil, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config"), nil, 0644))
	require.Equal(t, filepath.Join(dir, "config"), cargoFile(dir, "config"))
}

func TestFileURL(t *testing.T) {
	require.Equal(t, "file:///etc/pki/rpm-gpg/KEY%20A", fileURL("/etc/pki/rpm-gpg/KEY A"))
}

func TestSetNetrcMachineKeepsFollowingComments(t *testing.T) {
	existing := "machine packages.depot.dev login depot password old\n# keep this\nmachine example.com login a password b\n# trailing\n"
	out, err := setNetrcMachine([]byte(existing), "packages.depot.dev", "depot", "tok")
	require.NoError(t, err)
	require.Equal(t, "# keep this\nmachine example.com login a password b\n# trailing\nmachine packages.depot.dev login depot password tok\n", string(out))
}

func TestWriteGemSourcesNormalizesUpstream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gemrc")
	require.NoError(t, os.WriteFile(path, []byte("---\n:sources:\n- https://rubygems.org\n"), 0644))

	s := newTestSetup(t, "rubygems", "rubygems")
	s.replaceUpstream = true
	require.NoError(t, writeGemSources(s, path))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "---\n:sources:\n- \"https://depot:tok_secret@packages.depot.dev/org123/rubygems/rubygems/\"\n", string(data))
}

func TestWriteCargoConfigRejectsDottedReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("source.crates-io.replace-with = \"depot\"\n"), 0644))

	s := newTestSetup(t, "cargo", "internal")
	require.ErrorContains(t, writeCargoConfig(s, path, []string{"cargo:token"}), "can't update")
}

func TestNpmUserConfigPathExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NPM_CONFIG_USERCONFIG", "~/.npmrc-custom")
	path, err := npmUserConfigPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".npmrc-custom"), path)
}

func TestWriteCargoConfigKeepsQuotedDottedTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[\"registries.depot\"]\nx = 1\n"), 0644))

	s := newTestSetup(t, "cargo", "internal")
	require.NoError(t, writeCargoConfig(s, path, []string{"cargo:token"}))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "[\"registries.depot\"]\nx = 1\n\n[registries.depot]\n"))
}

func TestWriteCargoConfigUpdatesExistingSourceTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[source.crates-io]\n# note\nregistry = \"sparse+https://index.crates.io/\"\n"), 0644))

	s := newTestSetup(t, "cargo", "crates-io")
	s.replaceUpstream = true
	require.NoError(t, writeCargoConfig(s, path, []string{"cargo:token"}))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "[source.crates-io]\nreplace-with = \"depot\"\n# note\nregistry = \"sparse+https://index.crates.io/\"\n"), string(data))
	require.Equal(t, 1, strings.Count(string(data), "[source.crates-io]"))

	s.replaceUpstream = false
	s.replaceUpstreamExplicit = true
	require.NoError(t, writeCargoConfig(s, path, []string{"cargo:token"}))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "[source.crates-io]\n# note\nregistry = \"sparse+https://index.crates.io/\"\n"), string(data))
}

func TestSetNetrcMachineMatchesQuotedName(t *testing.T) {
	out, err := setNetrcMachine([]byte("machine \"packages.depot.dev\" login depot password old\n"), "packages.depot.dev", "depot", "tok")
	require.NoError(t, err)
	require.Equal(t, "machine packages.depot.dev login depot password tok\n", string(out))
}

func TestBunUserConfigPath(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NPM_CONFIG_USERCONFIG", filepath.Join(home, "ignored"))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	path, err := bunUserConfigPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".npmrc"), path)

	require.NoError(t, os.WriteFile(filepath.Join(xdg, ".npmrc"), nil, 0600))
	path, err = bunUserConfigPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(xdg, ".npmrc"), path)
}

func TestMambaConfigPath(t *testing.T) {
	t.Setenv("MAMBARC", "")
	t.Setenv("CONDARC", "")
	require.Equal(t, "/home/me/.mambarc", mambaConfigPath("/home/me"))
	t.Setenv("CONDARC", "~/condarc")
	require.Equal(t, "/home/me/condarc", mambaConfigPath("/home/me"))
	t.Setenv("MAMBARC", "/etc/mamba.yml")
	require.Equal(t, "/etc/mamba.yml", mambaConfigPath("/home/me"))
	t.Setenv("MAMBARC", "/etc/custom-rc")
	require.Equal(t, "/home/me/condarc", mambaConfigPath("/home/me"))
}

func TestListItemStripsInlineComments(t *testing.T) {
	item, ok := listItem("- https://rubygems.org/ # default source")
	require.True(t, ok)
	require.Equal(t, "https://rubygems.org/", item)

	item, ok = listItem(`- "https://a/#x" # note`)
	require.True(t, ok)
	require.Equal(t, `"https://a/#x"`, item)
}

func TestIsCondaUpstream(t *testing.T) {
	require.True(t, isCondaUpstream("https://repo.anaconda.com/pkgs/main/"))
	require.True(t, isCondaUpstream("conda-forge"))
	require.False(t, isCondaUpstream("bioconda"))
}

func TestNerfDartKeepsEscapedPath(t *testing.T) {
	t.Setenv("DEPOT_PACKAGES_URL", "https://host/base%20path")
	repo, err := newRepository("org123", "npm", "team")
	require.NoError(t, err)
	require.Equal(t, "//host/base%20path/org123/npm/team/", repo.nerfDart())
	require.Equal(t, "https://host/base%20path/org123/npm/team/", repo.url())
}

func TestGemConfigPathSkipsEmptyGEMRCEntries(t *testing.T) {
	t.Setenv("GEMRC", "/etc/team.gemrc"+string(os.PathListSeparator))
	path, err := gemConfigPath()
	require.NoError(t, err)
	require.Equal(t, "/etc/team.gemrc", path)
}

func TestWriteNpmrcRejectsRegistryEnvironmentOverride(t *testing.T) {
	t.Setenv("NPM_CONFIG_USERCONFIG", filepath.Join(t.TempDir(), ".npmrc"))
	t.Setenv("NPM_CONFIG_REGISTRY", "https://registry.npmjs.org/")

	s := newTestSetup(t, "npm", "npmjs")
	require.ErrorContains(t, writeNpmrc(s, ""), "NPM_CONFIG_REGISTRY")

	s.opts.scope = "@acme"
	require.NoError(t, writeNpmrc(s, ""))
}

func TestWriteCargoConfigRejectsOtherRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[registries.depot]\nindex = \"sparse+https://other.example/index/\"\n"), 0644))

	s := newTestSetup(t, "cargo", "internal")
	require.ErrorContains(t, writeCargoConfig(s, path, []string{"cargo:token"}), "already defines")
}

func TestNewRepositoryAcceptsUnderscoreOrgID(t *testing.T) {
	t.Setenv("DEPOT_PACKAGES_URL", "")
	_, err := newRepository("org_1a2b3c4d", "npm", "team")
	require.NoError(t, err)
}

func TestWriteCargoConfigRejectsInlineSourceTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[source]\ncrates-io = { registry = \"sparse+https://index.crates.io/\" }\n"), 0644))

	s := newTestSetup(t, "cargo", "crates-io")
	s.replaceUpstream = true
	require.ErrorContains(t, writeCargoConfig(s, path, []string{"cargo:token"}), "can't update")
}

func TestWriteCargoConfigRejectsIndexEnvironmentOverride(t *testing.T) {
	t.Setenv("CARGO_REGISTRIES_DEPOT_INDEX", "sparse+https://other.example/index/")
	s := newTestSetup(t, "cargo", "internal")
	require.ErrorContains(t, writeCargoConfig(s, filepath.Join(t.TempDir(), "config.toml"), []string{"cargo:token"}), "CARGO_REGISTRIES_DEPOT_INDEX")
}

func TestWriteNpmrcRejectsBunTokenOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BUN_CONFIG_TOKEN", "other")
	s := newTestSetup(t, "npm", "npmjs")
	s.clientName = "bun"
	require.ErrorContains(t, writeNpmrc(s, ""), "BUN_CONFIG_TOKEN")

	s.opts.scope = "@acme"
	require.NoError(t, writeNpmrc(s, ""))

	t.Setenv("BUN_CONFIG_TOKEN", "")
	t.Setenv("npm_config_token", "other")
	s.opts.scope = ""
	require.ErrorContains(t, writeNpmrc(s, ""), "npm_config_token")
}

func TestCheckProjectNpmrc(t *testing.T) {
	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, "package.json"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".npmrc"), []byte("registry=https://registry.npmjs.org/\n"), 0644))
	t.Chdir(project)

	user := filepath.Join(t.TempDir(), ".npmrc")
	require.ErrorContains(t, checkProjectNpmrc(user, map[string]string{"registry": "https://packages.depot.dev/org123/npm/npmjs/"}, npmProjectDirs()), "takes precedence")
	require.NoError(t, checkProjectNpmrc(user, map[string]string{"registry": "https://registry.npmjs.org"}, npmProjectDirs()))
	require.NoError(t, checkProjectNpmrc(user, map[string]string{"@acme:registry": "https://packages.depot.dev/org123/npm/npmjs/"}, npmProjectDirs()))
}

func TestCheckCargoOverridesRejectsProjectRegistry(t *testing.T) {
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".cargo"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".cargo", "config.toml"), []byte("[registries.depot]\nindex = \"sparse+https://attacker.example/\"\n"), 0644))
	t.Chdir(project)

	s := newTestSetup(t, "cargo", "internal")
	require.ErrorContains(t, checkCargoOverrides(s, t.TempDir()), "overrides the Cargo home configuration")
}

func TestIsRubygemsUpstream(t *testing.T) {
	for _, source := range []string{"https://rubygems.org/", "https://rubygems.org", "http://rubygems.org/", "https://rubygems.org:443/"} {
		require.True(t, isRubygemsUpstream(source), source)
	}
	require.False(t, isRubygemsUpstream("https://rubygems.org/other/"))
	require.False(t, isRubygemsUpstream("https://gems.example/"))
}

func TestNpmrcGetUsesLastAssignment(t *testing.T) {
	rc := parseNpmrc([]byte("registry=https://a/\nregistry=https://b/\n"))
	value, ok := rc.get("registry")
	require.True(t, ok)
	require.Equal(t, "https://b/", value)
}

func TestCheckProjectNpmrcFindsWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "pnpm-workspace.yaml"), nil, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".npmrc"), []byte("registry=https://registry.npmjs.org/\n"), 0644))
	pkg := filepath.Join(root, "packages", "a")
	require.NoError(t, os.MkdirAll(pkg, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pkg, "package.json"), []byte("{}"), 0644))
	t.Chdir(pkg)

	require.ErrorContains(t, checkProjectNpmrc(filepath.Join(t.TempDir(), ".npmrc"), map[string]string{"registry": "https://packages.depot.dev/org123/npm/npmjs/"}, npmProjectDirs()), filepath.Join(root, ".npmrc"))
}

func TestCheckBunfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bunfig.toml"), []byte("[install]\nregistry = \"https://registry.npmjs.org/\"\n"), 0644))
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	s := newTestSetup(t, "npm", "npmjs")
	require.ErrorContains(t, checkBunfig(s), "install.registry")
	s.opts.scope = "@acme"
	require.NoError(t, checkBunfig(s))
}

func TestCheckCargoProjectConfigFollowsIncludes(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte("include = [\"extra.toml\"]\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "extra.toml"), []byte("[source.depot]\nregistry = \"sparse+https://attacker.example/\"\n"), 0644))

	s := newTestSetup(t, "cargo", "internal")
	require.ErrorContains(t, checkCargoProjectConfig(s, filepath.Join(dir, "config.toml"), map[string]bool{}), "source")
}

func TestCheckCargoOverridesUsesActiveFile(t *testing.T) {
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".cargo"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".cargo", "config"), []byte("[net]\nretry = 1\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".cargo", "config.toml"), []byte("[registries.depot]\nindex = \"x\"\n"), 0644))
	t.Chdir(project)

	s := newTestSetup(t, "cargo", "internal")
	require.NoError(t, checkCargoOverrides(s, t.TempDir()))
}

func TestCheckBunfigScopeAndProjectRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "package.json"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "bunfig.toml"), []byte("[install.scopes]\n\"@acme\" = \"https://other.example/\"\n"), 0644))
	sub := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(sub, 0755))
	t.Chdir(sub)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	s := newTestSetup(t, "npm", "internal")
	s.opts.scope = "@acme"
	require.ErrorContains(t, checkBunfig(s), "install.scopes.@acme")
}

func TestCheckYarnLockfileFindsWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"workspaces":["packages/*"]}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "yarn.lock"), []byte("a@1:\n  resolved \"https://registry.yarnpkg.com/a/-/a-1.tgz\"\n"), 0644))
	pkg := filepath.Join(root, "packages", "a")
	require.NoError(t, os.MkdirAll(pkg, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pkg, "package.json"), []byte("{}"), 0644))
	t.Chdir(pkg)

	s := newTestSetup(t, "npm", "npmjs")
	checkYarnLockfile(s)
	require.Len(t, s.notes, 1)
	require.Contains(t, s.notes[0], filepath.Join(root, "yarn.lock"))
}

func TestCheckBunfigRejectsInlineCredentials(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bunfig.toml"), []byte("[install.registry]\nurl = \"https://packages.depot.dev/org123/npm/npmjs/\"\ntoken = \"stale\"\n"), 0644))
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	s := newTestSetup(t, "npm", "npmjs")
	require.ErrorContains(t, checkBunfig(s), "install.registry.token")
	s.token = "stale"
	require.NoError(t, checkBunfig(s))
}

func TestCheckPublishConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"publishConfig":{"registry":"https://registry.npmjs.org/"}}`), 0644))
	t.Chdir(dir)

	s := newTestSetup(t, "npm", "internal")
	checkPublishConfig(s)
	require.Len(t, s.notes, 1)
}

func TestCheckCargoOverridesFollowsHomeIncludes(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte("include = \"extra.toml\"\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(home, "extra.toml"), []byte("[source.depot]\nregistry = \"sparse+https://attacker.example/\"\n"), 0644))
	t.Chdir(t.TempDir())

	s := newTestSetup(t, "cargo", "crates-io")
	require.ErrorContains(t, checkCargoOverrides(s, home), "source")
}

func TestRubyConfigUsesLastAssignment(t *testing.T) {
	config := parseRubyConfig([]byte("---\n:sources:\n- \"https://a/\"\n:verbose: true\n:sources:\n- \"https://b/\"\n"))
	items, _, err := config.list(":sources")
	require.NoError(t, err)
	require.Equal(t, []string{"https://b/"}, items)

	config.setList(":sources", []string{"https://c/"})
	require.Equal(t, "---\n:verbose: true\n:sources:\n- \"https://c/\"\n", string(config.bytes()))

	config = parseRubyConfig([]byte("---\nKEY: \"a\"\nKEY: \"b\"\n"))
	value, _ := config.get("KEY")
	require.Equal(t, "b", value)
	config.remove("KEY")
	require.Equal(t, "---\n", string(config.bytes()))
}

func TestWriteCargoConfigRejectsHomeSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[source.depot]\nregistry = \"sparse+https://other.example/\"\n"), 0644))

	s := newTestSetup(t, "cargo", "crates-io")
	require.ErrorContains(t, writeCargoConfig(s, path, []string{"cargo:token"}), "source")
}

func TestCheckCargoProjectConfigFollowsLongIncludeChains(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("c%d.toml", i)), []byte(fmt.Sprintf("include = \"c%d.toml\"\n", i+1)), 0644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c20.toml"), []byte("include = \"c0.toml\"\n[source.depot]\nregistry = \"x\"\n"), 0644))

	s := newTestSetup(t, "cargo", "internal")
	require.ErrorContains(t, checkCargoProjectConfig(s, filepath.Join(dir, "c0.toml"), map[string]bool{}), "c20.toml")
}

func TestWriteMambarcKeepsPrivateDefaultChannelsAndAnchors(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mambarc")
	require.NoError(t, os.WriteFile(path, []byte("channels: &base\n  - defaults\nallowlist_channels: *base\ndefault_channels:\n  - https://mirror.example/main\n"), 0644))

	s := newTestSetup(t, "conda", "conda-forge")
	s.replaceUpstream = true
	isUpstream := func(c string) bool { return c != "defaults" && isCondaUpstream(c) }
	require.NoError(t, writeMambarc(s, path, isUpstream))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "channels: &base\n  - https://packages.depot.dev/org123/conda/conda-forge\n  - defaults\nallowlist_channels: *base\ndefault_channels:\n  - https://mirror.example/main\n", string(data))
}

func TestWriteMambaAuthenticationAcceptsNull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authentication.json")
	require.NoError(t, os.WriteFile(path, []byte("null"), 0600))
	s := newTestSetup(t, "conda", "conda-forge")
	require.NoError(t, writeMambaAuthentication(s, path))
}

func TestCheckBunfigUsesProjectBeforeGlobal(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bunfig.toml"), []byte("[install]\nregistry = \"https://packages.depot.dev/org123/npm/npmjs/\"\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".bunfig.toml"), []byte("[install]\nregistry = \"https://registry.npmjs.org/\"\n"), 0644))
	t.Chdir(dir)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	s := newTestSetup(t, "npm", "npmjs")
	require.NoError(t, checkBunfig(s))
}

func TestRPMRepositoryIDsDiffer(t *testing.T) {
	s := newTestSetup(t, "rpm", "internal")
	require.NotEqual(t, rpmRepositoryID(s.repo, "el/9"), rpmRepositoryID(s.repo, "el-9"))
}

func TestWriteNpmrcRejectsScopedRegistryEnvironmentOverride(t *testing.T) {
	t.Setenv("NPM_CONFIG_USERCONFIG", filepath.Join(t.TempDir(), ".npmrc"))
	t.Setenv("npm_config_@acme:registry", "https://registry.example/")
	t.Chdir(t.TempDir())

	s := newTestSetup(t, "npm", "internal")
	s.opts.scope = "@acme"
	require.ErrorContains(t, writeNpmrc(s, ""), "npm_config_@acme:registry")
}

func TestWriteMambarcFiltersPublicDefaultChannels(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mambarc")
	require.NoError(t, os.WriteFile(path, []byte("default_channels:\n  - https://mirror.example/main\n  - https://repo.anaconda.com/pkgs/main\n"), 0644))

	s := newTestSetup(t, "conda", "conda-forge")
	s.replaceUpstream = true
	require.NoError(t, writeMambarc(s, path, isCondaUpstream))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "default_channels:\n  - https://mirror.example/main\n")
	require.NotContains(t, string(data), "repo.anaconda.com")
}

func TestRPMRepositoryIDIncludesOrganization(t *testing.T) {
	t.Setenv("DEPOT_PACKAGES_URL", "")
	a, err := newRepository("org-a", "rpm", "internal")
	require.NoError(t, err)
	b, err := newRepository("org-b", "rpm", "internal")
	require.NoError(t, err)
	require.NotEqual(t, rpmRepositoryID(a, "el/9"), rpmRepositoryID(b, "el/9"))
}

func TestWriteMambarcRejectsRemovingAnchoredEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mambarc")
	require.NoError(t, os.WriteFile(path, []byte("channels:\n  - &public conda-forge\n  - bioconda\nallowlist_channels:\n  - *public\n"), 0644))

	s := newTestSetup(t, "conda", "conda-forge")
	s.replaceUpstream = true
	require.ErrorContains(t, writeMambarc(s, path, isCondaUpstream), "&public")

	require.NoError(t, os.WriteFile(path, []byte("channels:\n  - &bio bioconda\nallowlist_channels:\n  - *bio\n"), 0644))
	require.NoError(t, writeMambarc(s, path, isCondaUpstream))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "&bio bioconda")
}

func TestCheckProjectNpmrcChecksAlwaysAuth(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".npmrc"), []byte("//packages.depot.dev/org123/npm/npmjs/:always-auth=false\n"), 0644))
	t.Chdir(dir)
	key := "//packages.depot.dev/org123/npm/npmjs/:always-auth"
	require.ErrorContains(t, checkProjectNpmrc(filepath.Join(t.TempDir(), ".npmrc"), map[string]string{key: "true"}, ancestorDirs()), "always-auth")
}

func TestCheckPnpmConfigYAMLAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("PNPM_CONFIG_NPMRC_AUTH_FILE", "")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "pnpm"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pnpm", "config.yaml"), []byte("_auth:\n  https://packages.depot.dev/org123/npm/npmjs/:\n    \"@\":\n      authToken: stale\n"), 0600))

	s := newTestSetup(t, "npm", "npmjs")
	s.clientName = "pnpm"
	require.ErrorContains(t, checkPnpmAuthFile(s, nil), "_auth")
}

func TestFileIDsDiffer(t *testing.T) {
	t.Setenv("DEPOT_PACKAGES_URL", "")
	a, err := newRepository("acme-prod", "debian", "debian")
	require.NoError(t, err)
	b, err := newRepository("acme", "debian", "prod-debian")
	require.NoError(t, err)
	require.NotEqual(t, a.fileID(), b.fileID())
}

func TestCheckPnpmAuthFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.ini")
	require.NoError(t, os.WriteFile(path, []byte("//packages.depot.dev/org123/npm/npmjs/:_authToken=stale\n"), 0600))
	t.Setenv("PNPM_CONFIG_NPMRC_AUTH_FILE", path)

	s := newTestSetup(t, "npm", "npmjs")
	s.clientName = "pnpm"
	require.ErrorContains(t, checkPnpmAuthFile(s, []string{s.repo.nerfDart() + ":_authToken"}), "pnpm config delete")
}

func TestWriteMambarcResolvesAliases(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mambarc")
	require.NoError(t, os.WriteFile(path, []byte("public: &public conda-forge\nchannels:\n  - *public\n"), 0644))

	s := newTestSetup(t, "conda", "conda-forge")
	s.replaceUpstream = true
	require.NoError(t, writeMambarc(s, path, isCondaUpstream))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "*public")
}

func TestGemKeyNamesDiffer(t *testing.T) {
	t.Setenv("DEPOT_PACKAGES_URL", "")
	a, err := newRepository("acme-prod", "rubygems", "gems")
	require.NoError(t, err)
	b, err := newRepository("acme", "rubygems", "prod-gems")
	require.NoError(t, err)
	require.NotEqual(t, a.gemKeyName(), b.gemKeyName())
}

func TestCheckCargoProjectConfigStopsSymlinkCycles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Symlink(dir, filepath.Join(dir, "link")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte("include = \"link/config.toml\"\n"), 0644))

	s := newTestSetup(t, "cargo", "internal")
	require.NoError(t, checkCargoProjectConfig(s, filepath.Join(dir, "config.toml"), map[string]bool{}))
}

func TestWriteNpmrcRejectsCredentialEnvironmentOverride(t *testing.T) {
	t.Setenv("NPM_CONFIG_USERCONFIG", filepath.Join(t.TempDir(), ".npmrc"))
	t.Setenv("npm_config_//packages.depot.dev/org123/npm/npmjs/:_authToken", "stale")
	t.Chdir(t.TempDir())

	s := newTestSetup(t, "npm", "npmjs")
	require.ErrorContains(t, writeNpmrc(s, ""), ":_authToken")
}

func TestCheckPnpmAuthEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PNPM_CONFIG_NPMRC_AUTH_FILE", "")
	t.Setenv("pnpm_config__auth", `{"https://packages.depot.dev/org123/npm/":{"@":{"authToken":"stale"}}}`)

	s := newTestSetup(t, "npm", "npmjs")
	s.clientName = "pnpm"
	require.ErrorContains(t, checkPnpmAuthFile(s, nil), "pnpm_config__auth")
}

func TestCheckCargoOverridesAllowsSelfIncludingHome(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.Symlink(home, filepath.Join(home, "self")))
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte("include = \"self/config.toml\"\n[registries.depot]\nindex = \"sparse+https://packages.depot.dev/org123/cargo/internal/index/\"\n"), 0644))
	t.Chdir(t.TempDir())

	s := newTestSetup(t, "cargo", "internal")
	require.NoError(t, checkCargoOverrides(s, home))
}

func TestWriteCargoConfigKeepsReplacementOnImplicitRerun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	s := newTestSetup(t, "cargo", "mirror")
	s.replaceUpstream = true
	require.NoError(t, writeCargoConfig(s, path, []string{"cargo:token"}))

	s.replaceUpstream = false
	require.NoError(t, writeCargoConfig(s, path, []string{"cargo:token"}))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "replace-with")

	other := newTestSetup(t, "cargo", "internal")
	require.NoError(t, writeCargoConfig(other, path, []string{"cargo:token"}))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "replace-with")

	explicit := newTestSetup(t, "cargo", "mirror")
	explicit.replaceUpstream = true
	require.NoError(t, writeCargoConfig(explicit, path, []string{"cargo:token"}))
	explicit.replaceUpstream = false
	explicit.replaceUpstreamExplicit = true
	require.NoError(t, writeCargoConfig(explicit, path, []string{"cargo:token"}))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "replace-with")
}

func TestWriteNpmrcChecksEachCredentialVariable(t *testing.T) {
	t.Setenv("NPM_CONFIG_USERCONFIG", filepath.Join(t.TempDir(), ".npmrc"))
	t.Chdir(t.TempDir())
	s := newTestSetup(t, "npm", "npmjs")
	s.clientName = "pnpm"
	t.Setenv("npm_config_//packages.depot.dev/org123/npm/npmjs/:_authToken", "/helper")
	require.ErrorContains(t, writeNpmrc(s, "/helper"), ":_authToken")
}

func TestWriteNpmrcChecksAlwaysAuthVariable(t *testing.T) {
	t.Setenv("NPM_CONFIG_USERCONFIG", filepath.Join(t.TempDir(), ".npmrc"))
	t.Chdir(t.TempDir())
	key := "//packages.depot.dev/org123/npm/npmjs/:always-auth"
	t.Setenv("npm_config_"+key, "false")
	s := newTestSetup(t, "npm", "npmjs")
	require.ErrorContains(t, writeNpmrc(s, "", key), "always-auth")
}

func TestWriteNpmrcChecksNormalizedScopeVariable(t *testing.T) {
	t.Setenv("NPM_CONFIG_USERCONFIG", filepath.Join(t.TempDir(), ".npmrc"))
	t.Setenv("npm_config_@acme_team:registry", "https://registry.example/")
	t.Chdir(t.TempDir())
	s := newTestSetup(t, "npm", "internal")
	s.opts.scope = "@acme-team"
	require.ErrorContains(t, writeNpmrc(s, ""), "@acme_team")
}

func TestWriteCargoConfigRejectsEmptyIndexVariable(t *testing.T) {
	t.Setenv("CARGO_REGISTRIES_DEPOT_INDEX", "")
	s := newTestSetup(t, "cargo", "internal")
	require.ErrorContains(t, writeCargoConfig(s, filepath.Join(t.TempDir(), "config.toml"), []string{"cargo:token"}), "CARGO_REGISTRIES_DEPOT_INDEX")
}

func TestWriteMambarcRestoresAbsoluteCondaForge(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mambarc")
	require.NoError(t, os.WriteFile(path, []byte("channels:\n  - https://packages.depot.dev/org123/conda/conda-forge\n  - conda-forge\n"), 0644))
	s := newTestSetup(t, "conda", "conda-forge")
	s.replaceUpstreamExplicit = true
	aliased := func(c string) bool { return c != "conda-forge" && isCondaUpstream(c) }
	require.NoError(t, writeMambarc(s, path, aliased))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "https://conda.anaconda.org/conda-forge")
}

func TestWriteGemCredentialRejectsFlowMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	require.NoError(t, os.WriteFile(path, []byte("--- {:rubygems_api_key: abc}\n"), 0600))
	s := newTestSetup(t, "rubygems", "internal")
	require.ErrorContains(t, writeGemCredential(s, path), "one key on each line")
}
