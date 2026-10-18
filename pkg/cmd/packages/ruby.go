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

const (
	rubygemsUpstream = "https://rubygems.org/"
	gemKeyName       = "depot"
)

var plainYAMLValue = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func configureBundler(s *setup) error {
	path, err := bundlerConfigPath()
	if err != nil {
		return err
	}
	data, err := readFileIfExists(path)
	if err != nil {
		return err
	}
	config := parseRubyConfig(data)

	config.set(bundlerKey(s.repo.url()), "depot:"+s.token)

	mirrorKey := bundlerKey("mirror." + rubygemsUpstream)
	if s.replaceUpstream {
		config.set(mirrorKey, s.repo.url())
		s.notef("Bundler now downloads gems from %s through the repository.", rubygemsUpstream)
	} else {
		mirror, _ := config.get(mirrorKey)
		if mirror == s.repo.url() || (s.replaceUpstreamExplicit && strings.HasPrefix(mirror, s.repo.formatURL())) {
			config.remove(mirrorKey)
			s.notef("Removed the Bundler mirror for %s.", rubygemsUpstream)
		}
		s.notef("Use the repository in your Gemfile with:\n  source %q", s.repo.url())
	}

	return s.writeFile(path, config.bytes(), 0600)
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
// precedence: the last GEMRC path when set, ~/.gemrc when that file exists,
// and the XDG configuration directory otherwise.
func gemConfigPath() (string, error) {
	if paths := filepath.SplitList(os.Getenv("GEMRC")); len(paths) > 0 && paths[len(paths)-1] != "" {
		return paths[len(paths)-1], nil
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
	s.notef("Publish with:\n  gem push --key %s --host %s <gem-file>", gemKeyName, s.repo.url())
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

	sources, found, err := config.list(":sources")
	if err != nil {
		return fmt.Errorf("could not update %s: %w", path, err)
	}
	if !found {
		sources = effectiveGemSources(s)
	}

	var updated []string
	for _, source := range sources {
		if isRepositorySource(source, s.repo) || (s.replaceUpstream && source == rubygemsUpstream) {
			continue
		}
		updated = append(updated, source)
	}
	if s.replaceUpstreamExplicit && !s.replaceUpstream && !slices.Contains(updated, rubygemsUpstream) {
		updated = append([]string{rubygemsUpstream}, updated...)
		s.notef("Added %s back to the gem sources.", rubygemsUpstream)
	}
	updated = append(updated, s.repo.urlWithCredentials("depot", s.token))
	config.setList(":sources", updated)

	if !s.replaceUpstream && slices.Contains(updated, rubygemsUpstream) {
		s.notef("gem also uses %s. Rerun with --replace-upstream to remove it.", rubygemsUpstream)
	}
	return s.writeFile(path, config.bytes(), 0600)
}

// effectiveGemSources returns the sources RubyGems uses from all of its
// configuration files, such as /etc/gemrc, so writing the user file keeps them.
func effectiveGemSources(s *setup) []string {
	out, err := exec.CommandContext(s.ctx, "gem", "sources", "--list").Output()
	if err != nil {
		return []string{rubygemsUpstream}
	}
	var sources []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "://") {
			sources = append(sources, line)
		}
	}
	if len(sources) == 0 {
		return []string{rubygemsUpstream}
	}
	return sources
}

func isRepositorySource(source string, repo repository) bool {
	u, err := url.Parse(source)
	if err != nil {
		return false
	}
	u.User = nil
	return strings.TrimSuffix(u.String(), "/") == strings.TrimSuffix(repo.url(), "/")
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
	entry := ":" + gemKeyName + ": " + value

	var lines []string
	text := strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if text == "" {
		lines = []string{"---"}
	} else {
		lines = strings.Split(text, "\n")
	}

	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(line, ":"+gemKeyName+":") || strings.HasPrefix(line, gemKeyName+":") {
			lines[i] = entry
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, entry)
	}

	return s.writeFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}
