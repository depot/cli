package packages

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
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

// bunUserConfigPath returns the user file Bun reads. Bun 1.4.4 and later read
// NPM_CONFIG_USERCONFIG; otherwise Bun prefers $XDG_CONFIG_HOME/.npmrc when
// that file exists.
func bunUserConfigPath(s *setup) (string, error) {
	if bunReadsUserConfigVariable(s) {
		if path, err := npmUserConfigPath(); err == nil && (os.Getenv("NPM_CONFIG_USERCONFIG") != "" || os.Getenv("npm_config_userconfig") != "") {
			return path, nil
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

var bunVersionPattern = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)`)

func bunReadsUserConfigVariable(s *setup) bool {
	out, err := exec.CommandContext(s.ctx, "bun", "--version").Output()
	if err != nil {
		return false
	}
	match := bunVersionPattern.FindStringSubmatch(strings.TrimSpace(string(out)))
	if match == nil {
		return false
	}
	var version [3]int
	for i := range version {
		version[i], _ = strconv.Atoi(match[i+1])
	}
	return version[0] > 1 || (version[0] == 1 && (version[1] > 4 || (version[1] == 4 && version[2] >= 4)))
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
	if s.clientName == "bun" {
		path, err = bunUserConfigPath(s)
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
	expected := map[string]string{tokenKey: s.token, helperKey: helper}
	for _, key := range flagKeys {
		expected[key] = "true"
	}
	for _, key := range slices.Sorted(maps.Keys(expected)) {
		for _, name := range configEnvNames(s, key) {
			if value, ok := os.LookupEnv(name); ok && value != expected[key] {
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
	dirs := npmProjectDirs()
	if s.clientName == "yarn" {
		dirs = ancestorDirs()
	}
	if err := checkProjectNpmrc(path, managed, dirs); err != nil {
		return err
	}
	if err := checkPnpmAuthFile(s, npmCredentialKeys(s)); err != nil {
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
	if s.clientName == "yarn" && !s.opts.authOnly && s.opts.scope == "" {
		checkYarnLockfile(s)
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
// configuration key. pnpm 11 and later read pnpm_config_ instead of
// npm_config_.
func configEnvNames(s *setup, key string) []string {
	prefixes := []string{"npm_config_", "NPM_CONFIG_"}
	if s.clientName == "pnpm" {
		prefixes = append(prefixes, "pnpm_config_", "PNPM_CONFIG_")
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
// pnpm-workspace.yaml, which takes precedence over the user .npmrc.
func checkPnpmWorkspaceRegistry(s *setup) error {
	if s.clientName != "pnpm" || s.opts.authOnly {
		return nil
	}
	for _, dir := range npmProjectDirs() {
		path := filepath.Join(dir, "pnpm-workspace.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var workspace struct {
			Registry   string         `yaml:"registry"`
			Registries map[string]any `yaml:"registries"`
		}
		if yaml.Unmarshal(data, &workspace) != nil {
			continue
		}
		value, key := workspace.Registry, "registry"
		if s.opts.scope != "" {
			value, _ = workspace.Registries[s.opts.scope].(string)
			key = "registries." + s.opts.scope
		} else if def, ok := workspace.Registries["default"].(string); ok {
			value, key = def, "registries.default"
		}
		// Current pnpm also keys registries by URL, with the scopes routed
		// to each one; "@" is the default registry.
		scope := s.opts.scope
		if scope == "" {
			scope = "@"
		}
		for url, declaration := range workspace.Registries {
			fields, ok := declaration.(map[string]any)
			if !ok {
				continue
			}
			scopes, _ := fields["scopes"].([]any)
			if slices.Contains(scopes, any(scope)) {
				value, key = url, "registries."+url+".scopes"
			}
		}
		if value != "" && strings.TrimSuffix(value, "/") != strings.TrimSuffix(s.repo.url(), "/") {
			return fmt.Errorf("%s sets %s to %s, which takes precedence over ~/.npmrc for pnpm: remove it, then rerun this command", path, key, value)
		}
	}
	return nil
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
func checkProjectNpmrc(userPath string, keys map[string]string, dirs []string) error {
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
			value, ok := rc.get(key)
			if !ok || (keys[key] != "" && strings.TrimSuffix(value, "/") == strings.TrimSuffix(keys[key], "/")) {
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
func checkPnpmAuthFile(s *setup, keys []string) error {
	if s.clientName != "pnpm" {
		return nil
	}
	dir, err := pnpmConfigDir()
	if err != nil {
		return nil
	}

	authPath := os.Getenv("PNPM_CONFIG_NPMRC_AUTH_FILE")
	if authPath == "" {
		authPath = filepath.Join(dir, "auth.ini")
	}
	if data, err := os.ReadFile(authPath); err == nil {
		rc := parseNpmrc(data)
		for _, key := range keys {
			if _, ok := rc.get(key); ok {
				return fmt.Errorf("%s sets %s, which pnpm uses instead of ~/.npmrc: remove it with `pnpm config delete --location global %s`, then rerun this command", authPath, key, key)
			}
		}
	}

	for _, name := range []string{"pnpm_config__auth", "PNPM_CONFIG__AUTH"} {
		value := os.Getenv(name)
		if value == "" {
			continue
		}
		var auth map[string]any
		if yaml.Unmarshal([]byte(value), &auth) != nil {
			return fmt.Errorf("%s is set, and pnpm uses it instead of ~/.npmrc, but setup can't read it: unset it, then rerun this command", name)
		}
		if registry, ok := pnpmAuthCovers(s, auth); ok {
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
	if registry, ok := pnpmAuthCovers(s, config.Auth); ok {
		return fmt.Errorf("%s has a credential for %s under _auth, which pnpm uses instead of ~/.npmrc: remove that entry, then rerun this command", configPath, registry)
	}
	return nil
}

// pnpmAuthCovers returns the registry of a structured _auth entry that covers
// the repository.
func pnpmAuthCovers(s *setup, auth map[string]any) (string, bool) {
	for registry := range auth {
		u, err := url.Parse(registry)
		if err != nil {
			continue
		}
		scoped := "//" + u.Host + strings.TrimSuffix(u.EscapedPath(), "/") + "/"
		if strings.HasPrefix(s.repo.nerfDart(), scoped) {
			return registry, true
		}
	}
	return "", false
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

func npmProjectDirs() []string {
	dir, err := os.Getwd()
	if err != nil {
		return nil
	}
	var dirs []string
	project := ""
	for current := dir; ; current = filepath.Dir(current) {
		isPackage := fileExists(filepath.Join(current, "package.json")) || fileExists(filepath.Join(current, "node_modules"))
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
	for _, dir := range npmProjectDirs() {
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

// checkYarnLockfile warns about locked tarball URLs on the public registry,
// because Yarn Classic downloads them directly instead of from the configured
// registry.
func checkYarnLockfile(s *setup) {
	for _, dir := range npmProjectDirs() {
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
	dirs := npmProjectDirs()
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
