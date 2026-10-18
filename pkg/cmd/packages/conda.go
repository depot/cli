package packages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

var condaUpstreamChannels = []string{
	"conda-forge",
	"defaults",
	"https://conda.anaconda.org/conda-forge",
}

func (r repository) condaChannel() string {
	return strings.TrimSuffix(r.url(), "/")
}

func configureMamba(s *setup) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if err := writeMambaAuthentication(s, filepath.Join(home, ".mamba", "auth", "authentication.json")); err != nil {
		return err
	}
	if err := writeMambarc(s, filepath.Join(home, ".mambarc")); err != nil {
		return err
	}
	if s.replaceUpstream {
		return checkMambaUpstreamChannels(s)
	}
	return nil
}

// checkMambaUpstreamChannels finds upstream channels in other configuration
// files. The client combines channel lists from all of its files, so editing
// ~/.mambarc can't remove them. It runs after ~/.mambarc is updated because
// the client reports a duplicate channel only for the first file listing it.
func checkMambaUpstreamChannels(s *setup) error {
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
		if i >= len(config.Channels.Source) || config.Channels.Source[i] == "~/.mambarc" {
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
		return fmt.Errorf("~/.mambarc is updated, but %s. Remove those channels and rerun this command, or rerun with --replace-upstream=false", message)
	}
	s.notef("%s. Remove those channels to download all packages through the repository.", message)
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
	if err := s.runQuiet("pixi", "auth", "login", s.repo.hostname(), "--token", s.token); err != nil {
		return err
	}
	s.changes = append(s.changes, "Pixi credential store")

	if s.replaceUpstream {
		for _, channel := range condaUpstreamChannels {
			_ = s.runQuiet("pixi", "workspace", "channel", "remove", channel)
		}
	}
	if err := s.runQuiet("pixi", "workspace", "channel", "add", "--prepend", s.repo.condaChannel()); err != nil {
		return err
	}
	s.changes = append(s.changes, "Pixi workspace manifest")
	return nil
}
