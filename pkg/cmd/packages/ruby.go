package packages

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const rubygemsUpstream = "https://rubygems.org/"

// gemKeyName names the push credential per repository, because RubyGems key
// names are shared across all gem servers and "depot" may already be in use.
// Repository names can't contain underscores, so the separator keeps names
// unique.
func (r repository) gemKeyName() string {
	return "depot-" + r.org + "_" + r.name
}

var (
	plainYAMLValue    = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	gemCredentialLine = regexp.MustCompile(`^\S+: `)
)

func configureBundler(s *setup) error {
	if os.Getenv("BUNDLE_IGNORE_CONFIG") != "" {
		return fmt.Errorf("BUNDLE_IGNORE_CONFIG is set, so Bundler ignores its configuration files: unset it, then rerun this command")
	}

	path, err := bundlerConfigPath()
	if err != nil {
		return err
	}
	data, err := readFileIfExists(path)
	if err != nil {
		return err
	}
	config := parseRubyConfig(data)
	if err := config.validate(path); err != nil {
		return err
	}

	config.set(bundlerKey(s.repo.url()), "depot:"+s.token)

	mirrorKey := bundlerKey("mirror." + rubygemsUpstream)
	if s.replaceUpstream {
		config.set(mirrorKey, s.repo.url())
		s.notef("Bundler now downloads gems from %s through the repository.", rubygemsUpstream)
	} else {
		mirror, _ := config.get(mirrorKey)
		if s.replaceUpstreamExplicit && isDepotRubygemsURL(s, mirror) {
			config.remove(mirrorKey)
			s.notef("Removed the Bundler mirror for %s.", rubygemsUpstream)
		}
		s.notef("Use the repository in your Gemfile with:\n  source %q", s.repo.url())
	}

	if err := s.writeFile(path, config.bytes(), 0600); err != nil {
		return err
	}
	if err := checkBundlerCredential(s); err != nil {
		return err
	}
	if s.replaceUpstream {
		return checkBundlerMirror(s)
	}
	if s.replaceUpstreamExplicit {
		return checkBundlerMirrorRemoved(s)
	}
	return nil
}

// checkBundlerMirrorRemoved reports a Depot mirror that remains in effect
// after replacement is turned off, such as one in the application
// configuration. Mirrors outside Depot are left as they are.
func checkBundlerMirrorRemoved(s *setup) error {
	key := "mirror." + rubygemsUpstream
	out, err := exec.CommandContext(s.ctx, "bundle", "config", "get", "--parseable", key).Output()
	if err != nil {
		return nil
	}
	mirror := strings.TrimPrefix(strings.TrimSpace(string(out)), key+"=")
	if !isDepotRubygemsURL(s, mirror) {
		return nil
	}
	return fmt.Errorf("Bundler still uses the %s mirror %s from another configuration file: remove it with `bundle config unset --local %s`, then rerun this command", rubygemsUpstream, mirror, key)
}

// checkBundlerCredential rejects an application credential for the
// repository, which takes precedence over the user configuration.
func checkBundlerCredential(s *setup) error {
	key := s.repo.url()
	out, err := exec.CommandContext(s.ctx, "bundle", "config", "get", "--parseable", key).Output()
	if err != nil {
		return nil
	}
	if strings.TrimPrefix(strings.TrimSpace(string(out)), key+"=") == "depot:"+s.token {
		return nil
	}
	return fmt.Errorf("this application's Bundler configuration has another credential for %s, which takes precedence: remove it with `bundle config unset --local %s`, then rerun this command", key, key)
}

// checkBundlerMirror finds a mirror setting in the application configuration,
// which takes precedence over the user configuration that setup edits.
func checkBundlerMirror(s *setup) error {
	key := "mirror." + rubygemsUpstream
	out, err := exec.CommandContext(s.ctx, "bundle", "config", "get", "--parseable", key).Output()
	if err != nil {
		return nil
	}
	mirror := strings.TrimPrefix(strings.TrimSpace(string(out)), key+"=")
	if mirror == "" || mirror == s.repo.url() {
		return nil
	}

	message := fmt.Sprintf("this application's Bundler configuration sets the %s mirror to %s, which takes precedence", rubygemsUpstream, mirror)
	if s.replaceUpstreamExplicit {
		return fmt.Errorf("%s: remove it with `bundle config unset --local %s`, then rerun this command", message, key)
	}
	s.notef("In this directory, %s. Remove it with `bundle config unset --local %s` to use the repository.", message, key)
	return nil
}

// isDepotRubygemsURL reports whether a URL is a RubyGems repository on the
// Depot Packages server, in any organization.
func isDepotRubygemsURL(s *setup, value string) bool {
	path, ok := strings.CutPrefix(value, s.repo.server.String()+"/")
	if !ok {
		return false
	}
	parts := strings.Split(path, "/")
	return len(parts) >= 3 && parts[1] == "rubygems"
}

func bundlerConfigPath() (string, error) {
	for _, name := range []string{"BUNDLE_CONFIG", "BUNDLE_USER_CONFIG"} {
		if path := os.Getenv(name); path != "" {
			return path, nil
		}
	}
	if dir := os.Getenv("BUNDLE_USER_HOME"); dir != "" {
		return filepath.Join(dir, "config"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".bundle", "config"), nil
}

// gemConfigPath returns the RubyGems configuration file with the highest
// precedence: the last nonempty GEMRC path when set, ~/.gemrc when that file exists,
// and the XDG configuration directory otherwise.
func gemConfigPath() (string, error) {
	paths := filepath.SplitList(os.Getenv("GEMRC"))
	for i := len(paths) - 1; i >= 0; i-- {
		if paths[i] != "" {
			return paths[i], nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	legacy := filepath.Join(home, ".gemrc")
	if _, err := os.Stat(legacy); err == nil {
		return legacy, nil
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "gem", "gemrc"), nil
}

func configureGem(s *setup) error {
	configPath, err := gemConfigPath()
	if err != nil {
		return err
	}
	if err := writeGemSources(s, configPath); err != nil {
		return err
	}

	path, err := gemCredentialsPath()
	if err != nil {
		return err
	}
	if err := writeGemCredential(s, path); err != nil {
		return err
	}
	s.notef("Publish with:\n  gem push --key %s --host %s <gem-file>", s.repo.gemKeyName(), s.repo.url())
	if os.Getenv("GEM_HOST_API_KEY") != "" {
		s.notef("Unset GEM_HOST_API_KEY before you publish: gem push sends it instead of the --key credential.")
	}
	return nil
}

// writeGemSources edits the RubyGems configuration file directly, so the
// token never appears in a command line.
func writeGemSources(s *setup, path string) error {
	data, err := readFileIfExists(path)
	if err != nil {
		return err
	}
	config := parseRubyConfig(data)
	if err := config.validate(path); err != nil {
		return err
	}

	sources, found, err := config.list(":sources")
	if err != nil {
		return fmt.Errorf("could not update %s: %w", path, err)
	}
	if !found {
		if sources, err = effectiveGemSources(s); err != nil {
			return err
		}
	}

	var updated []string
	for _, source := range sources {
		if isRepositorySource(source, s.repo) || (s.replaceUpstream && isRubygemsUpstream(source)) {
			continue
		}
		updated = append(updated, source)
	}
	if s.replaceUpstreamExplicit && !s.replaceUpstream && !slices.ContainsFunc(updated, isRubygemsUpstream) {
		updated = append([]string{rubygemsUpstream}, updated...)
		s.notef("Added %s back to the gem sources.", rubygemsUpstream)
	}
	updated = append(updated, s.repo.urlWithCredentials("depot", s.token))
	config.setList(":sources", updated)

	if !s.replaceUpstream && slices.ContainsFunc(updated, isRubygemsUpstream) {
		s.notef("gem also uses %s. Rerun with --replace-upstream to remove it.", rubygemsUpstream)
	}
	return s.writeFile(path, config.bytes(), 0600)
}

// effectiveGemSources returns the sources RubyGems uses from all of its
// configuration files, such as /etc/gemrc, so writing the user file keeps them.
func effectiveGemSources(s *setup) ([]string, error) {
	out, err := exec.CommandContext(s.ctx, "gem", "sources", "--list").Output()
	if err != nil {
		return nil, fmt.Errorf("could not read the current gem sources with `gem sources --list`, which setup needs to keep them: %w", err)
	}
	var sources []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "://") {
			sources = append(sources, line)
		}
	}
	return sources, nil
}

// isRubygemsUpstream matches rubygems.org over HTTP or HTTPS, with or without
// the default port and trailing slash.
func isRubygemsUpstream(source string) bool {
	u, err := url.Parse(source)
	if err != nil || u.User != nil || (u.Path != "" && u.Path != "/") {
		return false
	}
	switch u.Scheme + "://" + u.Host {
	case "https://rubygems.org", "https://rubygems.org:443", "http://rubygems.org", "http://rubygems.org:80":
		return true
	}
	return false
}

func isRepositorySource(source string, repo repository) bool {
	u, err := url.Parse(source)
	if err != nil {
		return false
	}
	u.User = nil
	return strings.TrimSuffix(u.String(), "/") == strings.TrimSuffix(repo.url(), "/")
}

// rubygemsAPIKeysScript prints the string-valued API keys RubyGems uses, as
// YAML. Without a credentials file, RubyGems reads API keys from its
// configuration files, so a new credentials file must include them.
const rubygemsAPIKeysScript = `require "rubygems"
require "yaml"
config = Gem::ConfigFile.new([])
config.load_api_keys
keys = config.api_keys.select { |_, value| value.is_a?(String) }
keys[:rubygems_api_key] = keys.delete(:rubygems) if keys.key?(:rubygems)
print keys.to_yaml`

// rubygemsAPIKeys returns the API keys RubyGems reads from its configuration
// files, to copy into a new credentials file.
func rubygemsAPIKeys(s *setup) ([]byte, error) {
	out, err := exec.CommandContext(s.ctx, "ruby", "-e", rubygemsAPIKeysScript).Output()
	if err != nil {
		return nil, fmt.Errorf("could not read the API keys RubyGems uses, which a new credentials file must keep: %w", err)
	}
	if strings.TrimSpace(string(out)) == "--- {}" {
		return nil, nil
	}
	return out, nil
}

// gemCredentialsPath follows RubyGems: it uses ~/.gem/credentials when that
// file exists and the XDG data directory otherwise.
func gemCredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	legacy := filepath.Join(home, ".gem", "credentials")
	if _, err := os.Stat(legacy); err == nil {
		return legacy, nil
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "gem", "credentials"), nil
}

func writeGemCredential(s *setup, path string) error {
	data, err := readFileIfExists(path)
	if err != nil {
		return err
	}

	value := s.token
	if !plainYAMLValue.MatchString(value) {
		value = strconv.Quote(value)
	}
	keyName := s.repo.gemKeyName()
	entry := ":" + keyName + ": " + value

	if data == nil {
		if data, err = rubygemsAPIKeys(s); err != nil {
			return err
		}
	}

	var lines []string
	text := strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if text == "" {
		lines = []string{"---"}
	} else {
		lines = strings.Split(text, "\n")
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || (i == 0 && trimmed == "---") {
			continue
		}
		if !gemCredentialLine.MatchString(line) {
			return fmt.Errorf("could not update %s: setup can only edit a file with one key on each line, such as :rubygems_api_key: <key>", path)
		}
	}

	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(line, ":"+keyName+":") || strings.HasPrefix(line, keyName+":") {
			lines[i] = entry
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, entry)
	}

	return s.writeFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}
