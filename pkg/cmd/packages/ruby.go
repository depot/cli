package packages

import (
	"bytes"
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

// runQuiet runs a client command without echoing its output, because client
// output can contain the token.
func (s *setup) runQuiet(name string, args ...string) error {
	return s.runQuietWithInput(nil, name, args...)
}

func (s *setup) runQuietWithInput(input []byte, name string, args ...string) error {
	cmd := exec.CommandContext(s.ctx, name, args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(strings.ReplaceAll(string(out), s.token, "<token>"))
		return fmt.Errorf("%s %s failed: %w\n%s", name, args[0], err, message)
	}
	return nil
}

func configureBundler(s *setup) error {
	if err := s.runQuiet("bundle", "config", "set", "--global", s.repo.url(), "depot:"+s.token); err != nil {
		return err
	}
	s.changes = append(s.changes, "Bundler user configuration")

	mirrorKey := "mirror." + rubygemsUpstream
	if s.replaceUpstream {
		if err := s.runQuiet("bundle", "config", "set", "--global", mirrorKey, s.repo.url()); err != nil {
			return err
		}
		s.notef("Bundler now downloads gems from %s through the repository.", rubygemsUpstream)
	} else {
		out, err := exec.CommandContext(s.ctx, "bundle", "config", "get", "--parseable", mirrorKey).Output()
		mirror := strings.TrimPrefix(strings.TrimSpace(string(out)), mirrorKey+"=")
		isDepotMirror := mirror == s.repo.url() || (s.replaceUpstreamExplicit && strings.HasPrefix(mirror, s.repo.formatURL()))
		if err == nil && isDepotMirror {
			if err := s.runQuiet("bundle", "config", "unset", "--global", mirrorKey); err != nil {
				return err
			}
			s.notef("Removed the Bundler mirror for %s.", rubygemsUpstream)
		}
		s.notef("Use the repository in your Gemfile with:\n  source %q", s.repo.url())
	}

	path, err := bundlerConfigPath()
	if err != nil {
		return err
	}
	return restrictPermissions(path)
}

func bundlerConfigPath() (string, error) {
	if path := os.Getenv("BUNDLE_USER_CONFIG"); path != "" {
		return path, nil
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

// gemConfigPath follows RubyGems: it uses ~/.gemrc when that file exists and
// the XDG configuration directory otherwise.
func gemConfigPath() (string, error) {
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

// restrictPermissions limits a client's configuration file to its owner after
// the client has saved the token in it.
func restrictPermissions(path string) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	err := os.Chmod(path, 0600)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func configureGem(s *setup) error {
	out, err := exec.CommandContext(s.ctx, "gem", "sources", "--list").Output()
	if err != nil {
		return fmt.Errorf("could not list gem sources: %w", err)
	}

	for _, line := range strings.Split(string(out), "\n") {
		source := strings.TrimSpace(line)
		if !strings.HasPrefix(source, "http") {
			continue
		}
		u, err := url.Parse(source)
		if err != nil {
			continue
		}
		u.User = nil
		if strings.TrimSuffix(u.String(), "/") == strings.TrimSuffix(s.repo.url(), "/") {
			if err := s.runQuiet("gem", "sources", "--remove", source); err != nil {
				return err
			}
		}
	}

	if err := s.runQuiet("gem", "sources", "--add", s.repo.urlWithCredentials("depot", s.token)); err != nil {
		return err
	}
	s.changes = append(s.changes, "RubyGems user sources")

	if s.replaceUpstream {
		current, err := exec.CommandContext(s.ctx, "gem", "sources", "--list").Output()
		if err != nil {
			return fmt.Errorf("could not list gem sources: %w", err)
		}
		if slices.Contains(strings.Fields(string(current)), rubygemsUpstream) {
			if err := s.runQuiet("gem", "sources", "--remove", rubygemsUpstream); err != nil {
				return err
			}
		}
	} else {
		s.notef("gem still lists its other sources. Rerun with --replace-upstream to remove %s.", rubygemsUpstream)
	}

	configPath, err := gemConfigPath()
	if err != nil {
		return err
	}
	if err := restrictPermissions(configPath); err != nil {
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
