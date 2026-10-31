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

const condaForgeURL = "https://conda.anaconda.org/conda-forge"

var condaUpstreamChannels = []string{
	"conda-forge",
	"defaults",
	"https://conda.anaconda.org/conda-forge",
	"https://repo.anaconda.com/pkgs/main",
	"https://repo.anaconda.com/pkgs/r",
	"https://repo.anaconda.com/pkgs/msys2",
}

func isCondaUpstream(channel string) bool {
	return slices.Contains(condaUpstreamChannels, strings.TrimSuffix(channel, "/"))
}

// mambaUpstreamFilter returns a check for public upstream channels that takes
// channel_alias, custom_channels, and default_channels into account, because
// they can point a channel name such as conda-forge or defaults at a private
// mirror.
func mambaUpstreamFilter(s *setup) (func(string) bool, error) {
	out, err := exec.CommandContext(s.ctx, s.clientName, "config", "list", "channel_alias", "custom_channels", "default_channels", "--json").Output()
	if err != nil {
		if s.replaceUpstream {
			return nil, fmt.Errorf("could not read channel_alias and custom_channels with `%s config list`, which setup needs before it replaces channels: %w", s.clientName, err)
		}
		return isCondaUpstream, nil
	}
	var config struct {
		ChannelAlias    string            `json:"channel_alias"`
		CustomChannels  map[string]string `json:"custom_channels"`
		DefaultChannels []string          `json:"default_channels"`
	}
	if err := json.Unmarshal(out, &config); err != nil {
		if s.replaceUpstream {
			return nil, fmt.Errorf("could not read the output of `%s config list`: %w", s.clientName, err)
		}
		return isCondaUpstream, nil
	}
	aliased := config.ChannelAlias != "" && !isPublicCondaBase(config.ChannelAlias)

	// isNamedUpstream classifies a channel other than defaults.
	isNamedUpstream := func(channel string) bool {
		if !isCondaUpstream(channel) || channel == "defaults" {
			return false
		}
		if strings.Contains(channel, "://") {
			return true
		}
		if base, ok := config.CustomChannels[channel]; ok {
			return isPublicCondaBase(base)
		}
		return !aliased
	}

	return func(channel string) bool {
		if !isCondaUpstream(channel) {
			return false
		}
		if channel == "defaults" {
			return !slices.ContainsFunc(config.DefaultChannels, func(c string) bool { return !isNamedUpstream(c) })
		}
		return isNamedUpstream(channel)
	}, nil
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
	isUpstream, err := mambaUpstreamFilter(s)
	if err != nil {
		return err
	}
	if err := writeMambarc(s, path, isUpstream); err != nil {
		return err
	}
	if s.replaceUpstream {
		return checkMambaUpstreamChannels(s, path, home, isUpstream)
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
func checkMambaUpstreamChannels(s *setup, path, home string, isUpstream func(string) bool) error {
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
		if isUpstream(value) {
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
	if entries == nil {
		entries = map[string]any{}
	}
	entries[s.repo.host()] = map[string]string{"type": "BearerToken", "token": s.token}

	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return s.writeFile(path, append(out, '\n'), 0600)
}

func writeMambarc(s *setup, path string, isUpstream func(string) bool) error {
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
	nodes := []*yaml.Node{{Kind: yaml.ScalarNode, Value: channel}}
	if existing := yamlMappingValue(root, "channels"); existing != nil {
		if existing.Kind != yaml.SequenceNode {
			return fmt.Errorf("could not update %s: channels must be a list", path)
		}
		for _, item := range existing.Content {
			name := strings.TrimSuffix(yamlScalarValue(item), "/")
			if name == channel || (s.replaceUpstream && isUpstream(name)) {
				if err := checkRemovableYAMLNode(path, item); err != nil {
					return err
				}
				continue
			}
			channels = append(channels, yamlScalarValue(item))
			nodes = append(nodes, item)
		}
	}
	if s.replaceUpstreamExplicit && !s.replaceUpstream {
		if defaults := yamlMappingValue(root, "default_channels"); defaults != nil && defaults.Kind == yaml.SequenceNode && len(defaults.Content) == 0 && defaults.Anchor == "" {
			removeYAMLMappingValue(root, "default_channels")
		}
		// The absolute URL avoids a channel_alias or custom_channels entry that
		// points the conda-forge name at a mirror.
		if !slices.ContainsFunc(channels, isUpstream) {
			channels = append(channels, condaForgeURL)
			nodes = append(nodes, &yaml.Node{Kind: yaml.ScalarNode, Value: condaForgeURL})
			s.notef("Added %s back to the channels in %s.", condaForgeURL, path)
		}
	}
	setYAMLMappingValue(root, "channels", &yaml.Node{Kind: yaml.SequenceNode, Content: nodes})

	if s.replaceUpstream {
		var private []*yaml.Node
		if defaults := yamlMappingValue(root, "default_channels"); defaults != nil {
			if defaults.Kind == yaml.AliasNode {
				return fmt.Errorf("could not update %s: default_channels is an alias, which setup can't edit: list the channels directly, then rerun this command", path)
			}
			for _, item := range defaults.Content {
				if !isUpstream(yamlScalarValue(item)) {
					private = append(private, item)
				} else if err := checkRemovableYAMLNode(path, item); err != nil {
					return err
				}
			}
		}
		style := yaml.Style(0)
		if len(private) == 0 {
			style = yaml.FlowStyle
		}
		setYAMLMappingValue(root, "default_channels", &yaml.Node{Kind: yaml.SequenceNode, Style: style, Content: private})
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

// isPublicCondaBase reports whether a channel_alias or custom_channels base
// URL is the public anaconda.org channel host.
func isPublicCondaBase(base string) bool {
	switch strings.TrimSuffix(base, "/") {
	case "https://conda.anaconda.org", "http://conda.anaconda.org":
		return true
	}
	return false
}

// yamlScalarValue returns the value of a scalar node, following an alias to
// the anchored node.
func yamlScalarValue(node *yaml.Node) string {
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		return node.Alias.Value
	}
	return node.Value
}

// checkRemovableYAMLNode rejects removing an entry with an anchor, because
// other settings can refer to it and the file would no longer parse.
func checkRemovableYAMLNode(path string, node *yaml.Node) error {
	if node.Anchor != "" {
		return fmt.Errorf("could not update %s: the entry %s has the anchor &%s, which other settings can refer to: remove the anchor, then rerun this command", path, node.Value, node.Anchor)
	}
	return nil
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

// setYAMLMappingValue sets key to value. It updates an existing sequence in
// place, so an anchor on that sequence still has a definition for its aliases.
func setYAMLMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			existing := mapping.Content[i+1]
			if existing.Kind == yaml.SequenceNode && value.Kind == yaml.SequenceNode {
				existing.Content = value.Content
				return
			}
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
}

func configurePixi(s *setup) error {
	if err := writePixiCredentials(s); err != nil {
		return err
	}

	if err := s.runQuiet("pixi", "workspace", "channel", "add", "--prepend", s.repo.condaChannel()); err != nil {
		return err
	}
	if s.replaceUpstream {
		environments, err := pixiChannels(s)
		if err != nil {
			return err
		}
		// The default environment also lists channels that its features
		// declare. Pixi can't remove those without a feature scope, so they
		// are reported by checkPixiUpstreamChannels instead.
		for _, channel := range environments["default"] {
			if isCondaUpstream(channel) {
				err := s.runQuiet("pixi", "workspace", "channel", "remove", channel)
				if err != nil && !strings.Contains(err.Error(), "does not exist") {
					return err
				}
			}
		}
	}
	if s.replaceUpstreamExplicit && !s.replaceUpstream {
		if err := s.runQuiet("pixi", "workspace", "channel", "add", "conda-forge"); err != nil {
			return err
		}
	}
	s.changes = append(s.changes, "Pixi workspace manifest")

	if s.replaceUpstream {
		return checkPixiUpstreamChannels(s)
	}
	return nil
}

// checkPixiUpstreamChannels finds upstream channels that features of other
// environments still declare. Setup edits only the workspace channels.
func checkPixiUpstreamChannels(s *setup) error {
	environments, err := pixiChannels(s)
	if err != nil {
		return err
	}
	var found []string
	for environment, channels := range environments {
		for _, channel := range channels {
			if isCondaUpstream(channel) {
				found = append(found, fmt.Sprintf("%s in environment %s", channel, environment))
			}
		}
	}
	if len(found) == 0 {
		return nil
	}
	slices.Sort(found)

	message := fmt.Sprintf("Pixi still downloads directly from upstream channels: %s", strings.Join(found, ", "))
	if s.replaceUpstreamExplicit {
		return fmt.Errorf("the workspace channels are updated, but %s. Remove those channels from the features that declare them and rerun this command, or rerun with --replace-upstream=false", message)
	}
	s.notef("%s. Remove those channels from the features that declare them to download all packages through the repository.", message)
	return nil
}

// pixiChannels returns the channels of each workspace environment.
func pixiChannels(s *setup) (map[string][]string, error) {
	out, err := exec.CommandContext(s.ctx, "pixi", "workspace", "channel", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("could not list the Pixi workspace channels: %w", err)
	}
	environments := map[string][]string{}
	environment := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, "Environment:"); ok {
			environment = strings.TrimSpace(name)
			continue
		}
		if channel, ok := strings.CutPrefix(line, "- "); ok && environment != "" {
			environments[environment] = append(environments[environment], strings.TrimSpace(channel))
		}
	}
	return environments, nil
}

// writePixiCredentials stores the token with pixi auth login, which writes
// to the keyring when one is available and to the credential file otherwise.
// Pixi has no option to read the token from stdin or the environment.
func writePixiCredentials(s *setup) error {
	if err := s.runQuiet("pixi", "auth", "login", s.repo.hostname(), "--token", s.token); err != nil {
		return err
	}
	s.changes = append(s.changes, "Pixi credential store")
	return nil
}
