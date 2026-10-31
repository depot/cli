package packages

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

var npmScopePattern = regexp.MustCompile(`^@[a-z0-9][a-z0-9._~-]*$`)

func npmUserConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, name := range []string{"NPM_CONFIG_USERCONFIG", "npm_config_userconfig"} {
		if path := os.Getenv(name); path != "" {
			return expandHome(path, home), nil
		}
	}
	return filepath.Join(home, ".npmrc"), nil
}

var clientVersionPattern = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)`)

func clientVersion(ctx context.Context, name string) ([3]int, error) {
	var version [3]int
	out, err := exec.CommandContext(ctx, name, "--version").Output()
	if err != nil {
		return version, err
	}
	match := clientVersionPattern.FindStringSubmatch(strings.TrimSpace(string(out)))
	if match == nil {
		return version, fmt.Errorf("%s --version printed an unrecognized version", name)
	}
	for i := range version {
		version[i], _ = strconv.Atoi(match[i+1])
	}
	return version, nil
}

func versionAtLeast(version, minimum [3]int) bool {
	return slices.Compare(version[:], minimum[:]) >= 0
}

// pnpmVersionAtLeast compares the installed pnpm version. When the version
// can't be read, it assumes pnpm 11.0.0.
func (s *setup) pnpmVersionAtLeast(minimum [3]int) bool {
	if s.pnpmVersion == ([3]int{}) {
		version, err := clientVersion(s.ctx, "pnpm")
		if err != nil {
			version = [3]int{11, 0, 0}
		}
		s.pnpmVersion = version
	}
	return versionAtLeast(s.pnpmVersion, minimum)
}

// pnpmReadsPnpmConfigEnv reports whether pnpm reads pnpm_config_ environment
// variables, which pnpm 11 introduced.
func (s *setup) pnpmReadsPnpmConfigEnv() bool {
	return s.pnpmVersionAtLeast([3]int{11, 0, 0})
}

// pnpmUserConfigPath returns the user file pnpm 11 and later read, in this
// order: the npmrcAuthFile variable, the pnpm userconfig variable, npmrcAuthFile
// in the global config.yaml, the npm userconfig variable, then ~/.npmrc.
func pnpmUserConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, name := range []string{"PNPM_CONFIG_NPMRC_AUTH_FILE", "pnpm_config_npmrc_auth_file", "PNPM_CONFIG_USERCONFIG", "pnpm_config_userconfig"} {
		if path := os.Getenv(name); path != "" {
			return expandHome(path, home), nil
		}
	}
	dir, err := pnpmConfigDir()
	if err != nil {
		return "", err
	}
	configPath := filepath.Join(dir, "config.yaml")
	if data, err := os.ReadFile(configPath); err == nil {
		var config struct {
			NpmrcAuthFile string `yaml:"npmrcAuthFile"`
		}
		if yaml.Unmarshal(data, &config) == nil && config.NpmrcAuthFile != "" {
			if !filepath.IsAbs(config.NpmrcAuthFile) {
				return "", fmt.Errorf("%s sets npmrcAuthFile to %s, and pnpm doesn't expand ~ or relative paths there: set it to an absolute path, then rerun this command", configPath, config.NpmrcAuthFile)
			}
			return config.NpmrcAuthFile, nil
		}
	}
	return npmUserConfigPath()
}

// bunUserConfigPath returns the user file Bun reads. Bun 1.4.4 and later read
// NPM_CONFIG_USERCONFIG; otherwise Bun prefers $XDG_CONFIG_HOME/.npmrc when
// that file exists.
func bunUserConfigPath(s *setup) (string, error) {
	if os.Getenv("NPM_CONFIG_USERCONFIG") != "" || os.Getenv("npm_config_userconfig") != "" {
		version, err := clientVersion(s.ctx, "bun")
		if err != nil {
			return "", fmt.Errorf("could not read the Bun version, which decides whether Bun reads NPM_CONFIG_USERCONFIG: %w", err)
		}
		if versionAtLeast(version, [3]int{1, 4, 4}) {
			return npmUserConfigPath()
		}
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		path := filepath.Join(dir, ".npmrc")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".npmrc"), nil
}

// expandHome replaces a leading ~ with the home directory, as npm does for
// paths in its configuration.
func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return path
}

func configureNpm(s *setup) error {
	return writeNpmrc(s, "")
}

func configurePnpm(s *setup) error {
	if s.store {
		return writeNpmrc(s, "")
	}
	dir, err := depotBinDir()
	if err != nil {
		return err
	}
	if strings.ContainsAny(dir, " \t") {
		return fmt.Errorf("pnpm can't run a token helper whose path contains spaces (%s): rerun with --auth store to save the token instead", dir)
	}
	helper, err := s.installTokenHelper()
	if err != nil {
		return err
	}
	return writeNpmrc(s, helper)
}

func configureYarn(s *setup) error {
	out, err := exec.CommandContext(s.ctx, "yarn", "--version").Output()
	if err != nil {
		return fmt.Errorf("could not determine the Yarn version: %w", err)
	}
	version := strings.TrimSpace(string(out))
	major, _, _ := strings.Cut(version, ".")
	if n, err := strconv.Atoi(major); err != nil || n >= 2 {
		return fmt.Errorf("yarn %s isn't supported yet: setup supports Yarn Classic (1.x). For Yarn 2 and later, set npmRegistryServer and npmAuthToken in .yarnrc.yml", version)
	}
	return writeNpmrc(s, "", s.repo.nerfDart()+":always-auth")
}

// writeNpmrc saves routing and credentials in the user .npmrc. When helper is
// set, it configures pnpm's tokenHelper instead of saving the token. Each
// flag key is set to true; Yarn Classic needs always-auth to send the token
// for unscoped packages.
func writeNpmrc(s *setup, helper string, flagKeys ...string) error {
	if s.opts.scope != "" && !npmScopePattern.MatchString(s.opts.scope) {
		return fmt.Errorf("invalid --scope %q: use a package scope such as @acme", s.opts.scope)
	}
	if s.opts.scope != "" && s.opts.authOnly {
		return fmt.Errorf("--scope and --auth-only can't be used together")
	}

	path, err := npmUserConfigPath()
	switch s.clientName {
	case "bun":
		path, err = bunUserConfigPath(s)
	case "pnpm":
		if s.pnpmReadsPnpmConfigEnv() {
			path, err = pnpmUserConfigPath()
		}
	}
	if err != nil {
		return err
	}
	data, err := readFileIfExists(path)
	if err != nil {
		return err
	}
	rc := parseNpmrc(data)

	if !s.opts.authOnly {
		names := configEnvNames(s, "registry")
		if s.opts.scope != "" {
			names = nil
			for _, scope := range []string{s.opts.scope, strings.ReplaceAll(s.opts.scope, "-", "_")} {
				names = append(names, configEnvNames(s, scope+":registry")...)
			}
		} else if s.clientName == "bun" {
			names = append(names, "BUN_CONFIG_REGISTRY")
		} else if s.clientName == "yarn" {
			names = append(names, "YARN_REGISTRY", "yarn_registry")
		}
		for _, name := range names {
			if value := os.Getenv(name); value != "" && strings.TrimSuffix(value, "/") != strings.TrimSuffix(s.repo.url(), "/") {
				return fmt.Errorf("%s is set to %s, which overrides the registry in %s: unset it, or rerun with --auth-only", name, value, path)
			}
		}
	}

	// Bun applies these token variables to the default registry only.
	if s.clientName == "bun" && !s.opts.authOnly && s.opts.scope == "" {
		for _, name := range []string{"BUN_CONFIG_TOKEN", "NPM_CONFIG_TOKEN", "npm_config_token"} {
			if value := os.Getenv(name); value != "" && value != s.token {
				return fmt.Errorf("%s is set, and Bun sends it instead of the token in %s: unset it, then rerun this command", name, path)
			}
		}
	}

	if !s.opts.authOnly {
		key := registryKey(s)
		if previous, ok := rc.get(key); ok && previous != s.repo.url() {
			s.notef("Replaced %s=%s in %s.", key, previous, path)
		}
		rc.set(key, s.repo.url())
	}

	tokenKey := s.repo.nerfDart() + ":_authToken"
	helperKey := s.repo.nerfDart() + ":tokenHelper"
	// pnpm ignores tokenHelper settings from the environment, and npm doesn't
	// use tokenHelper, so only the token key is checked.
	expected := map[string]string{tokenKey: s.token}
	for _, key := range flagKeys {
		expected[key] = "true"
	}
	if s.opts.scope != "" {
		expected[s.repo.nerfDart()+":"+s.opts.scope+":_authToken"] = ""
	}
	for _, key := range slices.Sorted(maps.Keys(expected)) {
		for _, name := range configEnvNames(s, key) {
			// With the pnpm helper, any token variable takes precedence over
			// the helper, so even the current token would go stale.
			value, ok := os.LookupEnv(name)
			if ok && (value != expected[key] || (helper != "" && key == tokenKey)) {
				return fmt.Errorf("%s is set, which overrides %s in %s: unset it, then rerun this command", name, key, path)
			}
		}
	}
	managed := map[string]string{}
	for _, key := range npmCredentialKeys(s) {
		managed[key] = ""
	}
	if !s.opts.authOnly {
		managed[registryKey(s)] = s.repo.url()
	}
	for _, key := range flagKeys {
		managed[key] = "true"
	}
	if err := checkProjectNpmrc(s, path, managed, npmrcProjectDirs(s)); err != nil {
		return err
	}
	if err := checkPnpmAuthFile(s, path, npmCredentialKeys(s)); err != nil {
		return err
	}
	if err := checkPnpmWorkspaceRegistry(s); err != nil {
		return err
	}
	if s.clientName == "bun" && !s.opts.authOnly {
		if err := checkBunfig(s); err != nil {
			return err
		}
	}
	if s.clientName == "yarn" && !s.opts.authOnly {
		if err := checkYarnrcRegistry(s); err != nil {
			return err
		}
		if s.opts.scope == "" {
			checkYarnLockfile(s)
		}
	}
	checkPublishConfig(s)
	if s.opts.scope != "" {
		rc.remove(s.repo.nerfDart() + ":" + s.opts.scope + ":_authToken")
	}
	if helper != "" {
		rc.remove(tokenKey)
		rc.set(helperKey, helper)
	} else {
		rc.remove(helperKey)
		rc.set(tokenKey, s.token)
	}
	for _, key := range flagKeys {
		rc.set(key, "true")
	}

	return s.writeFile(path, rc.bytes(), 0600)
}

// configEnvNames returns the environment variable names that set the npm
// configuration key. pnpm 11 and later read pnpm_config_ for settings, and
// both prefixes for URL-scoped credential keys.
func configEnvNames(s *setup, key string) []string {
	prefixes := []string{"npm_config_", "NPM_CONFIG_"}
	if s.clientName == "pnpm" && s.pnpmReadsPnpmConfigEnv() {
		pnpmPrefixes := []string{"pnpm_config_", "PNPM_CONFIG_"}
		if strings.HasPrefix(key, "//") {
			prefixes = append(prefixes, pnpmPrefixes...)
		} else {
			prefixes = pnpmPrefixes
		}
	}
	var names []string
	for _, prefix := range prefixes {
		names = append(names, prefix+key)
		if upper := strings.ToUpper(key); upper != key && prefix == strings.ToUpper(prefix) {
			names = append(names, prefix+upper)
		}
	}
	return names
}

// checkPnpmWorkspaceRegistry rejects a registry or scope registry in
// pnpm-workspace.yaml or pnpm's global config.yaml, which take precedence over
// the user .npmrc. pnpm uses the first of those files that sets the key.
func checkPnpmWorkspaceRegistry(s *setup) error {
	if s.clientName != "pnpm" || s.opts.authOnly {
		return nil
	}
	var paths []string
	for _, dir := range npmProjectDirs(s) {
		paths = append(paths, filepath.Join(dir, "pnpm-workspace.yaml"))
	}
	if dir, err := pnpmConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "config.yaml"))
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		key, value := pnpmRegistrySetting(s, data)
		if key == "" {
			continue
		}
		if strings.TrimSuffix(value, "/") != strings.TrimSuffix(s.repo.url(), "/") {
			return fmt.Errorf("%s sets %s to %s, which takes precedence over ~/.npmrc for pnpm: remove it, then rerun this command", path, key, value)
		}
		return nil
	}
	return nil
}

// pnpmRegistrySetting returns the registry setting for the repository's scope,
// or for the default registry without a scope, from a pnpm YAML file. It
// returns an empty key when the file doesn't set one.
func pnpmRegistrySetting(s *setup, data []byte) (key, value string) {
	var config struct {
		Registry   string         `yaml:"registry"`
		Registries map[string]any `yaml:"registries"`
	}
	if yaml.Unmarshal(data, &config) != nil {
		return "", ""
	}
	if s.opts.scope == "" && config.Registry != "" {
		key, value = "registry", config.Registry
	}
	name := s.opts.scope
	if name == "" {
		name = "default"
	}
	if url, ok := config.Registries[name].(string); ok {
		key, value = "registries."+name, url
	}
	// Current pnpm also keys registries by URL, with the scopes routed to
	// each one; "@" is the default registry.
	scope := s.opts.scope
	if scope == "" {
		scope = "@"
	}
	for url, declaration := range config.Registries {
		fields, ok := declaration.(map[string]any)
		if !ok {
			continue
		}
		scopes, _ := fields["scopes"].([]any)
		if slices.Contains(scopes, any(scope)) {
			key, value = "registries."+url+".scopes", url
		}
	}
	return key, value
}

// npmCredentialKeys returns the credential keys for the repository, including
// pnpm's scope-specific form when a scope is set.
func npmCredentialKeys(s *setup) []string {
	keys := []string{s.repo.nerfDart() + ":_authToken", s.repo.nerfDart() + ":tokenHelper"}
	if s.opts.scope != "" {
		keys = append(keys, s.repo.nerfDart()+":"+s.opts.scope+":_authToken")
	}
	return keys
}

func registryKey(s *setup) string {
	if s.opts.scope != "" {
		return s.opts.scope + ":registry"
	}
	return "registry"
}

// checkProjectNpmrc rejects a project or workspace .npmrc that sets one of
// the managed keys to another value, because those files take precedence over
// the user configuration. The project directory is the nearest directory with
// a package.json or node_modules, as npm determines it. Workspace roots are
// parent directories with a pnpm-workspace.yaml or a package.json that
// declares workspaces. Yarn Classic reads .npmrc in every ancestor directory,
// so callers pass the directories that the client reads.
// pnpmIgnoresProjectEntry reports whether pnpm ignores an entry in a project
// .npmrc. pnpm never reads tokenHelper there, and pnpm 11.5.3 and later ignore
// registry and credential entries that contain ${...} references.
func pnpmIgnoresProjectEntry(s *setup, key, raw string) bool {
	if s.clientName != "pnpm" {
		return false
	}
	if strings.HasSuffix(key, ":tokenHelper") {
		return true
	}
	protected := key == "registry" || strings.HasSuffix(key, ":registry") || strings.HasPrefix(key, "//")
	return protected && strings.Contains(raw, "${") && s.pnpmVersionAtLeast([3]int{11, 5, 3})
}

func checkProjectNpmrc(s *setup, userPath string, keys map[string]string, dirs []string) error {
	for _, dir := range dirs {
		path := filepath.Join(dir, ".npmrc")
		if sameFile(path, userPath) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rc := parseNpmrc(data)
		for _, key := range slices.Sorted(maps.Keys(keys)) {
			raw, ok := rc.getRaw(key)
			if !ok || pnpmIgnoresProjectEntry(s, key, raw) {
				continue
			}
			value := expandNpmrcEnv(raw)
			if keys[key] != "" && strings.TrimSuffix(value, "/") == strings.TrimSuffix(keys[key], "/") {
				continue
			}
			return fmt.Errorf("%s sets %s, which takes precedence over %s in this project: remove that setting, then rerun this command", path, key, userPath)
		}
	}
	return nil
}

// checkPnpmAuthFile rejects a repository credential in pnpm's own auth.ini,
// in the pnpm_config__auth environment variable, or in the _auth section of
// its global config.yaml, all of which pnpm reads before the user .npmrc.
func checkPnpmAuthFile(s *setup, userPath string, keys []string) error {
	if s.clientName != "pnpm" {
		return nil
	}
	dir, err := pnpmConfigDir()
	if err != nil {
		return nil
	}

	// The auth file can be the user .npmrc that setup writes, which isn't an
	// override.
	authPath := pnpmAuthFilePath(dir)
	if data, err := os.ReadFile(authPath); err == nil && !sameFile(authPath, userPath) {
		rc := parseNpmrc(data)
		for _, key := range keys {
			if _, ok := rc.get(key); ok {
				return fmt.Errorf("%s sets %s, which pnpm uses instead of ~/.npmrc: remove it with `pnpm config delete --location global %s`, then rerun this command", authPath, key, key)
			}
		}
	}

	name := "pnpm_config__auth"
	if os.Getenv(name) == "" {
		name = "PNPM_CONFIG__AUTH"
	}
	if value := os.Getenv(name); value != "" {
		var auth map[string]any
		if yaml.Unmarshal([]byte(value), &auth) != nil {
			return fmt.Errorf("%s is set, and pnpm uses it instead of ~/.npmrc, but setup can't read it: unset it, then rerun this command", name)
		}
		if registry, ok := pnpmAuthCovers(s, auth, true); ok {
			return fmt.Errorf("%s has a credential for %s, which pnpm uses instead of ~/.npmrc: unset it or remove that entry, then rerun this command", name, registry)
		}
	}

	configPath := filepath.Join(dir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	var config struct {
		Auth map[string]any `yaml:"_auth"`
	}
	if yaml.Unmarshal(data, &config) != nil {
		return nil
	}
	if registry, ok := pnpmAuthCovers(s, config.Auth, false); ok {
		return fmt.Errorf("%s has a credential for %s under _auth, which pnpm uses instead of ~/.npmrc: remove that entry, then rerun this command", configPath, registry)
	}
	return nil
}

// pnpmAuthCovers returns the registry of a structured _auth entry that has a
// credential for the repository or, when routing is true, routes the
// configured scope ("@" for the default registry) to another registry. Only
// the environment form of _auth overrides an explicit registry setting.
func pnpmAuthCovers(s *setup, auth map[string]any, routing bool) (string, bool) {
	scope := s.opts.scope
	if scope == "" {
		scope = "@"
	}
	for registry, entry := range auth {
		u, err := url.Parse(registry)
		if err != nil {
			continue
		}
		scoped := "//" + u.Host + strings.TrimSuffix(u.EscapedPath(), "/") + "/"
		if strings.HasPrefix(s.repo.nerfDart(), scoped) {
			return registry, true
		}
		if scopes, ok := entry.(map[string]any); ok && routing && !s.opts.authOnly {
			if _, routed := scopes[scope]; routed {
				return registry, true
			}
		}
	}
	return "", false
}

// pnpmAuthFilePath returns the auth file pnpm reads: the npmrcAuthFile
// setting from the environment or the global config.yaml, or auth.ini in the
// configuration directory.
func pnpmAuthFilePath(dir string) string {
	home, _ := os.UserHomeDir()
	for _, name := range []string{"PNPM_CONFIG_NPMRC_AUTH_FILE", "pnpm_config_npmrc_auth_file"} {
		if path := os.Getenv(name); path != "" {
			return expandHome(path, home)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "config.yaml")); err == nil {
		var config struct {
			NpmrcAuthFile string `yaml:"npmrcAuthFile"`
		}
		if yaml.Unmarshal(data, &config) == nil && config.NpmrcAuthFile != "" {
			return expandHome(config.NpmrcAuthFile, home)
		}
	}
	return filepath.Join(dir, "auth.ini")
}

// pnpmConfigDir returns pnpm's global configuration directory.
func pnpmConfigDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "pnpm"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Preferences", "pnpm"), nil
	case "windows":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "pnpm", "config"), nil
	}
	return filepath.Join(home, ".config", "pnpm"), nil
}

// npmProjectMarkers are the files that mark a project directory for npm. pnpm
// also accepts pnpmProjectMarkers.
var (
	npmProjectMarkers  = []string{"package.json", "node_modules"}
	pnpmProjectMarkers = []string{"package.json", "package.yaml", "package.json5", "pnpm-workspace.yaml", "node_modules"}
)

func npmProjectDirs(s *setup) []string {
	markers := npmProjectMarkers
	if s.clientName == "pnpm" {
		markers = pnpmProjectMarkers
	}
	dir, err := os.Getwd()
	if err != nil {
		return nil
	}
	var dirs []string
	project := ""
	for current := dir; ; current = filepath.Dir(current) {
		isPackage := slices.ContainsFunc(markers, func(marker string) bool { return fileExists(filepath.Join(current, marker)) })
		if project == "" && isPackage {
			project = current
			dirs = append(dirs, current)
		} else if project != "" && isWorkspaceRoot(current) {
			dirs = append(dirs, current)
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	if project == "" {
		dirs = append(dirs, dir)
	}
	return dirs
}

// npmrcProjectDirs returns the directories whose .npmrc the client reads. npm
// uses the workspace root as the project and ignores the .npmrc of a workspace
// member.
func npmrcProjectDirs(s *setup) []string {
	if s.clientName == "yarn" {
		return ancestorDirs()
	}
	dirs := npmProjectDirs(s)
	if s.clientName == "npm" && len(dirs) > 1 && slices.ContainsFunc(dirs[1:], func(root string) bool { return npmWorkspaceIncludes(root, dirs[0]) }) {
		return dirs[1:]
	}
	return dirs
}

// npmWorkspaceIncludes reports whether a workspaces pattern in the package.json
// of root matches dir.
func npmWorkspaceIncludes(root, dir string) bool {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return false
	}
	var manifest struct {
		Workspaces []string `json:"workspaces"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return false
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	segments := strings.Split(filepath.ToSlash(rel), "/")
	included := false
	for _, pattern := range manifest.Workspaces {
		negated := strings.HasPrefix(pattern, "!")
		if matchGlobSegments(strings.Split(path.Clean(strings.TrimPrefix(pattern, "!")), "/"), segments) {
			if negated {
				return false
			}
			included = true
		}
	}
	return included
}

// matchGlobSegments matches path segments against a glob in which ** matches
// any number of segments, as npm workspace patterns do.
func matchGlobSegments(pattern, segments []string) bool {
	if len(pattern) == 0 {
		return len(segments) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(segments); i++ {
			if matchGlobSegments(pattern[1:], segments[i:]) {
				return true
			}
		}
		return false
	}
	if len(segments) == 0 {
		return false
	}
	ok, _ := path.Match(pattern[0], segments[0])
	return ok && matchGlobSegments(pattern[1:], segments[1:])
}

// ancestorDirs returns the current directory and each of its parents.
func ancestorDirs() []string {
	dir, err := os.Getwd()
	if err != nil {
		return nil
	}
	var dirs []string
	for {
		dirs = append(dirs, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			return dirs
		}
		dir = parent
	}
}

func isWorkspaceRoot(dir string) bool {
	if fileExists(filepath.Join(dir, "pnpm-workspace.yaml")) {
		return true
	}
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var manifest map[string]any
	if json.Unmarshal(data, &manifest) != nil {
		return false
	}
	_, ok := manifest["workspaces"]
	return ok
}

// checkBunfig rejects a bunfig.toml registry setting, which Bun reads after
// .npmrc, when it points to another registry. Bun uses the first file that
// sets the key, in this order: the current directory, the project and
// workspace directories, and the global configuration.
func checkBunfig(s *setup) error {
	var paths []string
	if dir, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(dir, "bunfig.toml"))
	}
	for _, dir := range npmProjectDirs(s) {
		paths = append(paths, filepath.Join(dir, "bunfig.toml"))
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		paths = append(paths, filepath.Join(dir, ".bunfig.toml"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".bunfig.toml"))
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var values map[string]any
		if toml.Unmarshal(data, &values) != nil {
			continue
		}
		key := []string{"install", "registry"}
		value, ok := tomlLookup(values, key...)
		if s.opts.scope != "" {
			for _, name := range []string{s.opts.scope, strings.TrimPrefix(s.opts.scope, "@")} {
				key = []string{"install", "scopes", name}
				if value, ok = tomlLookup(values, key...); ok {
					break
				}
			}
		}
		if !ok {
			continue
		}
		url, _ := value.(string)
		table, isTable := value.(map[string]any)
		if isTable {
			url, _ = table["url"].(string)
		}
		if strings.TrimSuffix(url, "/") != strings.TrimSuffix(s.repo.url(), "/") {
			return fmt.Errorf("%s sets %s, which takes precedence over .npmrc for Bun: remove that setting, then rerun this command", path, strings.Join(key, "."))
		}
		for _, field := range []string{"token", "username", "password"} {
			if value, ok := table[field].(string); ok && !(field == "token" && value == s.token) {
				return fmt.Errorf("%s sets %s.%s, which Bun sends instead of the token in .npmrc: remove it, then rerun this command", path, strings.Join(key, "."), field)
			}
		}
		return nil
	}
	return nil
}

// checkYarnrcRegistry rejects a registry in a .yarnrc file, which Yarn
// Classic reads before .npmrc. Yarn reads .yarnrc in the current directory,
// each parent, and the home directory.
func checkYarnrcRegistry(s *setup) error {
	key := "registry"
	if s.opts.scope != "" {
		key = s.opts.scope + ":registry"
	}
	dirs := ancestorDirs()
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, home)
	}
	for _, dir := range dirs {
		path := filepath.Join(dir, ".yarnrc")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			name, value, found := strings.Cut(strings.TrimSpace(line), " ")
			if !found || strings.Trim(strings.TrimPrefix(strings.Trim(name, `"`), "--"), `"`) != key {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			if strings.TrimSuffix(value, "/") != strings.TrimSuffix(s.repo.url(), "/") {
				return fmt.Errorf("%s sets %s to %s, which Yarn uses instead of .npmrc: remove it, then rerun this command", path, key, value)
			}
		}
	}
	return nil
}

// checkYarnLockfile warns about locked tarball URLs on the public registry,
// because Yarn Classic downloads them directly instead of from the configured
// registry.
func checkYarnLockfile(s *setup) {
	for _, dir := range npmProjectDirs(s) {
		path := filepath.Join(dir, "yarn.lock")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, host := range []string{"https://registry.yarnpkg.com/", "https://registry.npmjs.org/"} {
			if strings.Contains(string(data), `resolved "`+host) {
				s.notef("%s pins packages to %s, and Yarn downloads those URLs directly. To download them through the repository, run:\n  sed -i.bak 's#%s#%s#g' %s", path, host, host, s.repo.url(), path)
			}
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// checkPublishConfig reports a package.json publishConfig registry outside
// Depot Packages, because it overrides the configured registry for publish.
func checkPublishConfig(s *setup) {
	dirs := npmProjectDirs(s)
	if len(dirs) == 0 {
		return
	}
	path := filepath.Join(dirs[0], "package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var manifest struct {
		PublishConfig struct {
			Registry string `json:"registry"`
		} `json:"publishConfig"`
	}
	if json.Unmarshal(data, &manifest) != nil || manifest.PublishConfig.Registry == "" {
		return
	}
	if !strings.HasPrefix(manifest.PublishConfig.Registry, s.repo.formatURL()) {
		s.notef("%s publishes to %s through publishConfig.registry, not through Depot Packages.", path, manifest.PublishConfig.Registry)
	}
}
