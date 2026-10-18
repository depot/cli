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
	require.Equal(t, "machine example.com\n  login a\n  # comment\n  password b\ndefault login anon password x\nmachine packages.depot.dev login depot password tok_new\n", string(out))

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
