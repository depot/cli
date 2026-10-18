package packages

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// bunUserConfigPath returns the user file Bun reads. Bun doesn't read
// NPM_CONFIG_USERCONFIG, and it prefers $XDG_CONFIG_HOME/.npmrc when that file
// exists.
func bunUserConfigPath() (string, error) {
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

	userConfigPath := npmUserConfigPath
	if s.clientName == "bun" {
		userConfigPath = bunUserConfigPath
	}
	path, err := userConfigPath()
	if err != nil {
		return err
	}
	data, err := readFileIfExists(path)
	if err != nil {
		return err
	}
	rc := parseNpmrc(data)

	if !s.opts.authOnly && s.opts.scope == "" {
		names := []string{"NPM_CONFIG_REGISTRY", "npm_config_registry"}
		if s.clientName == "bun" {
			names = append(names, "BUN_CONFIG_REGISTRY")
		}
		for _, name := range names {
			if value := os.Getenv(name); value != "" && strings.TrimSuffix(value, "/") != strings.TrimSuffix(s.repo.url(), "/") {
				return fmt.Errorf("%s is set to %s, which overrides the registry in %s: unset it, or rerun with --scope or --auth-only", name, value, path)
			}
		}
	}

	if s.clientName == "bun" {
		for _, name := range []string{"BUN_CONFIG_TOKEN", "NPM_CONFIG_TOKEN"} {
			if value := os.Getenv(name); value != "" && value != s.token {
				return fmt.Errorf("%s is set, and Bun sends it instead of the token in %s: unset it, then rerun this command", name, path)
			}
		}
	}

	if !s.opts.authOnly {
		key := "registry"
		if s.opts.scope != "" {
			key = s.opts.scope + ":registry"
		}
		if previous, ok := rc.get(key); ok && previous != s.repo.url() {
			s.notef("Replaced %s=%s in %s.", key, previous, path)
		}
		rc.set(key, s.repo.url())
	}

	tokenKey := s.repo.nerfDart() + ":_authToken"
	helperKey := s.repo.nerfDart() + ":tokenHelper"
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
