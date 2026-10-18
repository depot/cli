package packages

import (
	"context"
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
	original := "# keep\n[net]\ngit-fetch-with-cli = true\n\n[registries.depot]\nindex = \"old\"\n\n[source.crates-io]\nreplace-with = \"depot\"\n"
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
	require.NoError(t, os.WriteFile(path, []byte("[registries]\ndepot = { index = \"x\" }\n"), 0644))

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
	require.NoError(t, writeMambarc(s, path))

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
	require.Equal(t, "---\n:rubygems_api_key: abc\n:depot: tok_secret\n", string(data))
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
	require.NoError(t, writeMambarc(s, path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "channels:\n  - https://packages.depot.dev/org123/conda/conda-forge\n  - conda-forge\n", string(data))
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
