package packages

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const cargoRegistryName = "depot"

func cargoHome() (string, error) {
	if dir := os.Getenv("CARGO_HOME"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cargo"), nil
}

// cargoFile returns the Cargo configuration file that Cargo reads. Cargo
// prefers the legacy name without an extension when both files exist.
func cargoFile(dir, name string) string {
	legacy := filepath.Join(dir, name)
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return legacy + ".toml"
}

func configureCargo(s *setup) error {
	dir, err := cargoHome()
	if err != nil {
		return err
	}

	if err := checkCargoOverrides(s, dir); err != nil {
		return err
	}

	provider := []string{"cargo:token"}
	if !s.store {
		if err := requireCargoTokenFromStdout(s); err != nil {
			return err
		}
		depot, stable, err := depotExecutable()
		if err != nil {
			return err
		}
		if !stable {
			s.notef("Cargo runs %s to obtain the token. Rerun this command if that path changes, for example after an upgrade.", depot)
		}
		provider = []string{"cargo:token-from-stdout", depot, "packages", "token"}
	}

	if err := writeCargoConfig(s, cargoFile(dir, "config"), provider); err != nil {
		return err
	}
	return writeCargoCredentials(s, cargoFile(dir, "credentials"))
}

// checkCargoOverrides rejects settings that take precedence over the Cargo
// home configuration for the depot registry: environment variables and
// project configuration files in the current directory and its parents.
func checkCargoOverrides(s *setup, home string) error {
	if os.Getenv("CARGO_REGISTRIES_DEPOT_CREDENTIAL_PROVIDER") != "" {
		return fmt.Errorf("CARGO_REGISTRIES_DEPOT_CREDENTIAL_PROVIDER is set, which overrides the credential provider setup configures: unset it, then rerun this command")
	}
	if s.store && os.Getenv("CARGO_REGISTRIES_DEPOT_TOKEN") != "" {
		return fmt.Errorf("CARGO_REGISTRIES_DEPOT_TOKEN is set, which overrides the token setup saves: unset it, then rerun this command")
	}

	dir, err := os.Getwd()
	if err != nil {
		return nil
	}
	homeConfig := cargoFile(home, "config")
	visited := map[string]bool{canonicalPath(homeConfig): true}
	if err := checkCargoIncludes(s, homeConfig, visited); err != nil {
		return err
	}
	for {
		path := cargoFile(filepath.Join(dir, ".cargo"), "config")
		if !sameFile(path, homeConfig) {
			if err := checkCargoProjectConfig(s, path, map[string]bool{}); err != nil {
				return err
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// checkCargoProjectConfig rejects project settings that would redirect the
// depot registry or source, including settings in included files.
func checkCargoProjectConfig(s *setup, path string, visited map[string]bool) error {
	path = canonicalPath(path)
	if visited[path] {
		return nil
	}
	visited[path] = true
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var values map[string]any
	if toml.Unmarshal(data, &values) != nil {
		return nil
	}

	if _, ok := tomlLookup(values, "registries", cargoRegistryName); ok {
		return fmt.Errorf("%s defines the %q registry, which overrides the Cargo home configuration in this directory: remove it or rename that registry, then rerun this command", path, cargoRegistryName)
	}
	if _, ok := tomlLookup(values, "source", cargoRegistryName); ok {
		return fmt.Errorf("%s defines a %q source, which overrides the Depot registry in this directory: remove it or rename that source, then rerun this command", path, cargoRegistryName)
	}
	if replacement, ok := tomlLookup(values, "source", "crates-io", "replace-with"); ok && s.replaceUpstream && replacement != cargoRegistryName {
		return fmt.Errorf("%s replaces crates.io with %q, which overrides the replacement in the Cargo home configuration: remove it, then rerun this command", path, replacement)
	}

	return checkCargoIncludes(s, path, visited)
}

// checkCargoIncludes checks the files that the configuration file at path
// includes, following the whole include chain and skipping files it has
// already checked. Setup edits only the top-level home file, so settings in
// its included files can also redirect the depot registry or source.
func checkCargoIncludes(s *setup, path string, visited map[string]bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var values map[string]any
	if toml.Unmarshal(data, &values) != nil {
		return nil
	}
	for _, include := range cargoIncludes(values["include"]) {
		if !filepath.IsAbs(include) {
			include = filepath.Join(filepath.Dir(path), include)
		}
		if err := checkCargoProjectConfig(s, include, visited); err != nil {
			return err
		}
	}
	return nil
}

// canonicalPath resolves symbolic links, so that one file has one path in
// the include scan.
func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// cargoIncludes returns the paths of an include setting, which can be a
// string, a list of strings, or a list of tables with a path key.
func cargoIncludes(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		var paths []string
		for _, item := range v {
			switch i := item.(type) {
			case string:
				paths = append(paths, i)
			case map[string]any:
				if path, ok := i["path"].(string); ok {
					paths = append(paths, path)
				}
			}
		}
		return paths
	}
	return nil
}

var cargoVersionPattern = regexp.MustCompile(`cargo ([0-9]+)\.([0-9]+)`)

// requireCargoTokenFromStdout checks for Cargo 1.79 or later. Earlier versions
// keep the trailing newline from cargo:token-from-stdout in the token.
func requireCargoTokenFromStdout(s *setup) error {
	out, err := exec.CommandContext(s.ctx, "cargo", "--version").Output()
	if err != nil {
		return fmt.Errorf("could not determine the Cargo version: %w", err)
	}
	match := cargoVersionPattern.FindStringSubmatch(string(out))
	if match == nil {
		return fmt.Errorf("could not determine the Cargo version from %q", strings.TrimSpace(string(out)))
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	if major == 1 && minor < 79 {
		return fmt.Errorf("cargo %s.%s can't use the Depot credential helper: upgrade to Cargo 1.79 or later, or rerun with --auth store", match[1], match[2])
	}
	return nil
}

func writeCargoConfig(s *setup, path string, provider []string) error {
	data, err := readFileIfExists(path)
	if err != nil {
		return err
	}
	doc, err := parseTOMLDocument(data)
	if err != nil {
		return fmt.Errorf("could not parse %s: %w", path, err)
	}

	registryTable := "registries." + cargoRegistryName
	if value, ok := os.LookupEnv("CARGO_REGISTRIES_DEPOT_INDEX"); ok && value != "sparse+"+s.repo.url()+"index/" {
		return fmt.Errorf("CARGO_REGISTRIES_DEPOT_INDEX is set to %s, which overrides the index in %s: unset it, then rerun this command", value, path)
	}
	existing, err := doc.values()
	if err != nil {
		return err
	}
	if _, ok := tomlLookup(existing, "source", cargoRegistryName); ok {
		return fmt.Errorf("%s defines a %q source, which takes precedence over the %q registry for source replacement: remove or rename that source, then rerun this command", path, cargoRegistryName, cargoRegistryName)
	}
	index := "sparse+" + s.repo.url() + "index/"
	previousIndex := ""
	if value, ok := tomlLookup(existing, "registries", cargoRegistryName, "index"); ok {
		previousIndex, _ = value.(string)
		if !strings.HasPrefix(previousIndex, "sparse+"+s.repo.server.String()+"/") {
			return fmt.Errorf("%s already defines the %q registry with index %v: rename that registry, then rerun this command", path, cargoRegistryName, value)
		}
	}
	// A rerun for the same repository keeps an earlier opt-in to crates.io
	// replacement unless --replace-upstream=false is passed. For another
	// repository the replacement is removed, because it would route
	// crates.io to that repository.
	if replacement, ok := tomlLookup(existing, "source", "crates-io", "replace-with"); ok && replacement == cargoRegistryName && !s.replaceUpstreamExplicit {
		if previousIndex == index {
			s.replaceUpstream = true
		} else {
			s.notef("Removed the crates.io replacement, because the %q registry now points to %s. Rerun with --replace-upstream if this repository includes crates.io.", cargoRegistryName, s.repo.url())
		}
	}
	doc.removeTable("registries", cargoRegistryName)

	values, err := doc.values()
	if err != nil {
		return err
	}
	if _, ok := tomlLookup(values, "registries", cargoRegistryName); ok {
		return errUnmanagedTOML(path, registryTable)
	}

	replacement, replaced := tomlLookup(values, "source", "crates-io", "replace-with")
	if replaced && replacement == cargoRegistryName {
		doc.removeTableKey([]string{"source", "crates-io"}, "replace-with")
		values, err = doc.values()
		if err != nil {
			return err
		}
		replacement, replaced = tomlLookup(values, "source", "crates-io", "replace-with")
		if replaced && replacement == cargoRegistryName {
			return errUnmanagedTOML(path, "source.crates-io.replace-with")
		}
	}

	doc.appendTable(registryTable, [][2]string{
		{"index", tomlString(index)},
		{"credential-provider", tomlStringArray(provider...)},
	})

	switch {
	case s.replaceUpstream && replaced:
		return fmt.Errorf("%s already replaces crates.io with %q; remove that source replacement and rerun this command", path, replacement)
	case s.replaceUpstream:
		if _, defined := tomlLookup(values, "source", "crates-io"); defined {
			if _, _, hasTable := doc.tableRange([]string{"source", "crates-io"}); !hasTable {
				return errUnmanagedTOML(path, "source.crates-io")
			}
		}
		doc.setTableKey([]string{"source", "crates-io"}, "replace-with", tomlString(cargoRegistryName))
		s.notef("Publish with `cargo publish --registry %s`. Source replacement doesn't apply to publishing.", cargoRegistryName)
	default:
		s.notef("Use the repository with `--registry %s`, or rerun with --replace-upstream if it includes crates.io.", cargoRegistryName)
	}

	return s.writeFile(path, doc.bytes(), configFileMode(path, 0644))
}

func writeCargoCredentials(s *setup, path string) error {
	data, err := readFileIfExists(path)
	if err != nil {
		return err
	}
	if data == nil && !s.store {
		return nil
	}

	doc, err := parseTOMLDocument(data)
	if err != nil {
		return fmt.Errorf("could not parse %s: %w", path, err)
	}

	registryTable := "registries." + cargoRegistryName
	doc.removeTable("registries", cargoRegistryName)

	values, err := doc.values()
	if err != nil {
		return err
	}
	if _, ok := tomlLookup(values, "registries", cargoRegistryName); ok {
		return errUnmanagedTOML(path, registryTable)
	}

	if s.store {
		doc.appendTable(registryTable, [][2]string{{"token", tomlString(s.token)}})
	}

	if string(doc.bytes()) == string(data) {
		return nil
	}
	return s.writeFile(path, doc.bytes(), 0600)
}
