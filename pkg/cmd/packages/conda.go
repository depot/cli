package packages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var mambaVersionPattern = regexp.MustCompile(`(?m)^(?:mamba )?([0-9]+)\.[0-9]+`)

var condaUpstreamChannels = []string{
	"conda-forge",
	"defaults",
	"https://conda.anaconda.org/conda-forge",
}

func (r repository) condaChannel() string {
	return strings.TrimSuffix(r.url(), "/")
}

func configureMamba(s *setup) error {
	if s.clientName == "mamba" {
		if err := requireMambaWithOwnConfig(s); err != nil {
			return err
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if err := writeMambaAuthentication(s, filepath.Join(home, ".mamba", "auth", "authentication.json")); err != nil {
		return err
	}
	path := mambaConfigPath(home)
	if err := writeMambarc(s, path); err != nil {
		return err
	}
	if s.replaceUpstream {
		return checkMambaUpstreamChannels(s, path, home)
	}
	return nil
}

// mambaConfigPath returns the configuration file with the highest precedence:
// MAMBARC, then CONDARC, then ~/.mambarc. The clients ignore an override whose
// file name isn't a configuration file name.
func mambaConfigPath(home string) string {
	for _, name := range []string{"MAMBARC", "CONDARC"} {
		if path := os.Getenv(name); path != "" && isMambaConfigName(filepath.Base(path)) {
			return expandHome(path, home)
		}
	}
	return filepath.Join(home, ".mambarc")
}

func isMambaConfigName(name string) bool {
	switch name {
	case "condarc", ".condarc", "mambarc", ".mambarc":
		return true
	}
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

// checkMambaUpstreamChannels finds upstream channels in other configuration
// files. The client combines channel lists from all of its files, so editing
// one file can't remove them. It runs after the file is updated because the
// client reports a duplicate channel only for the first file listing it.
func checkMambaUpstreamChannels(s *setup, path, home string) error {
	out, err := exec.CommandContext(s.ctx, s.clientName, "config", "list", "channels", "--sources", "--json").Output()
	if err != nil {
		s.notef("Could not check the other %s configuration files for upstream channels: %v.", s.clientName, err)
		return nil
	}

	var config struct {
		Channels struct {
			Source []string `json:"source"`
			Value  []string `json:"value"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(out, &config); err != nil {
		s.notef("Could not check the other %s configuration files for upstream channels: %v.", s.clientName, err)
		return nil
	}

	var found []string
	for i, value := range config.Channels.Value {
		if i >= len(config.Channels.Source) || filepath.Clean(expandHome(config.Channels.Source[i], home)) == filepath.Clean(path) {
			continue
		}
		if slices.Contains(condaUpstreamChannels, strings.TrimSuffix(value, "/")) {
			found = append(found, fmt.Sprintf("%s in %s", value, config.Channels.Source[i]))
		}
	}
	if len(found) == 0 {
		return nil
	}

	message := fmt.Sprintf("%s still downloads directly from upstream channels listed in other configuration files: %s", s.clientName, strings.Join(found, ", "))
	if s.replaceUpstreamExplicit {
		return fmt.Errorf("%s is updated, but %s. Remove those channels and rerun this command, or rerun with --replace-upstream=false", path, message)
	}
	s.notef("%s. Remove those channels to download all packages through the repository.", message)
	return nil
}

// requireMambaWithOwnConfig rejects mamba 1.x, which runs as part of conda and
// reads conda's configuration instead of the mamba configuration files.
func requireMambaWithOwnConfig(s *setup) error {
	out, err := exec.CommandContext(s.ctx, "mamba", "--version").Output()
	if err != nil {
		return fmt.Errorf("could not determine the mamba version: %w", err)
	}
	match := mambaVersionPattern.FindStringSubmatch(string(out))
	if match == nil {
		return fmt.Errorf("could not determine the mamba version from %q", strings.TrimSpace(string(out)))
	}
	if major, _ := strconv.Atoi(match[1]); major < 2 {
		return fmt.Errorf("mamba %s.x reads conda's configuration, which setup doesn't support yet: upgrade to mamba 2 or use micromamba", match[1])
	}
	return nil
}

func writeMambaAuthentication(s *setup, path string) error {
	data, err := readFileIfExists(path)
	if err != nil {
		return err
	}

	entries := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &entries); err != nil {
			return fmt.Errorf("could not parse %s: %w", path, err)
		}
	}
	// mamba 2.9 and micromamba 2.3 match credentials by host only and ignore
	// keys that include a path.
	entries[s.repo.host()] = map[string]string{"type": "BearerToken", "token": s.token}

	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return s.writeFile(path, append(out, '\n'), 0600)
}

func writeMambarc(s *setup, path string) error {
	data, err := readFileIfExists(path)
	if err != nil {
		return err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("could not parse %s: %w", path, err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("could not update %s: expected a mapping at the top level", path)
	}

	channel := s.repo.condaChannel()
	channels := []string{channel}
	if existing := yamlMappingValue(root, "channels"); existing != nil {
		if existing.Kind != yaml.SequenceNode {
			return fmt.Errorf("could not update %s: channels must be a list", path)
		}
		for _, item := range existing.Content {
			name := strings.TrimSuffix(item.Value, "/")
			if name == channel || (s.replaceUpstream && slices.Contains(condaUpstreamChannels, name)) {
				continue
			}
			channels = append(channels, item.Value)
		}
	}
	if s.replaceUpstreamExplicit && !s.replaceUpstream {
		if defaults := yamlMappingValue(root, "default_channels"); defaults != nil && defaults.Kind == yaml.SequenceNode && len(defaults.Content) == 0 {
			removeYAMLMappingValue(root, "default_channels")
		}
		if !slices.ContainsFunc(channels, func(c string) bool { return slices.Contains(condaUpstreamChannels, strings.TrimSuffix(c, "/")) }) {
			channels = append(channels, "conda-forge")
			s.notef("Added conda-forge back to the channels in %s.", path)
		}
	}
	setYAMLMappingValue(root, "channels", yamlStringSequence(channels, yaml.Style(0)))

	if s.replaceUpstream {
		setYAMLMappingValue(root, "default_channels", yamlStringSequence(nil, yaml.FlowStyle))
	}

	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}

	if len(channels) > 1 {
		s.notef("%s also lists other channels after the repository: %s.", path, strings.Join(channels[1:], ", "))
	}
	return s.writeFile(path, out.Bytes(), configFileMode(path, 0644))
}

func yamlMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func removeYAMLMappingValue(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

func setYAMLMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
}

func yamlStringSequence(values []string, style yaml.Style) *yaml.Node {
	node := &yaml.Node{Kind: yaml.SequenceNode, Style: style}
	for _, v := range values {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
	}
	return node
}

func configurePixi(s *setup) error {
	if err := writePixiCredentials(s); err != nil {
		return err
	}

	if s.replaceUpstream {
		channels, err := pixiChannels(s)
		if err != nil {
			return err
		}
		for _, channel := range channels {
			if slices.Contains(condaUpstreamChannels, strings.TrimSuffix(channel, "/")) {
				if err := s.runQuiet("pixi", "workspace", "channel", "remove", channel); err != nil {
					return err
				}
			}
		}
	}
	if err := s.runQuiet("pixi", "workspace", "channel", "add", "--prepend", s.repo.condaChannel()); err != nil {
		return err
	}
	if s.replaceUpstreamExplicit && !s.replaceUpstream {
		if err := s.runQuiet("pixi", "workspace", "channel", "add", "conda-forge"); err != nil {
			return err
		}
	}
	s.changes = append(s.changes, "Pixi workspace manifest")
	return nil
}

// pixiChannels returns the channels of the default environment.
func pixiChannels(s *setup) ([]string, error) {
	out, err := exec.CommandContext(s.ctx, "pixi", "workspace", "channel", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("could not list the Pixi workspace channels: %w", err)
	}
	var channels []string
	inDefault := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if environment, ok := strings.CutPrefix(line, "Environment:"); ok {
			inDefault = strings.TrimSpace(environment) == "default"
			continue
		}
		if channel, ok := strings.CutPrefix(line, "- "); ok && inDefault {
			channels = append(channels, strings.TrimSpace(channel))
		}
	}
	return channels, nil
}

// writePixiCredentials saves the token in the credential file Pixi reads, so
// the token never appears in a command line. It first tries to remove a stored
// keyring entry for the host, which Pixi would otherwise use instead.
func writePixiCredentials(s *setup) error {
	host := s.repo.hostname()
	if err := s.runQuiet("pixi", "auth", "logout", host); err != nil && !strings.Contains(err.Error(), "No stored credentials") {
		s.notef("Could not remove stored Pixi credentials for %s (%v). If Pixi sends an older token, run `pixi auth logout %s`.", host, err, host)
	}

	path := os.Getenv("RATTLER_AUTH_FILE")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, ".rattler", "credentials.json")
	}

	data, err := readFileIfExists(path)
	if err != nil {
		return err
	}
	entries := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &entries); err != nil {
			return fmt.Errorf("could not parse %s: %w", path, err)
		}
	}
	entries[host] = map[string]string{"BearerToken": s.token}

	out, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return s.writeFile(path, out, 0600)
}
