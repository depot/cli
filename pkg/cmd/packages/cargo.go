package packages

import (
	"fmt"
	"os"
	"path/filepath"
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

	provider := []string{"cargo:token"}
	if !s.store {
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
	doc.removeTable(registryTable)

	values, err := doc.values()
	if err != nil {
		return err
	}
	if _, ok := tomlLookup(values, "registries", cargoRegistryName); ok {
		return errUnmanagedTOML(path, registryTable)
	}

	replacement, replaced := tomlLookup(values, "source", "crates-io", "replace-with")
	if replaced && replacement == cargoRegistryName {
		doc.removeTable("source.crates-io")
		values, err = doc.values()
		if err != nil {
			return err
		}
		replacement, replaced = tomlLookup(values, "source", "crates-io", "replace-with")
	}

	doc.appendTable(registryTable, [][2]string{
		{"index", tomlString("sparse+" + s.repo.url() + "index/")},
		{"credential-provider", tomlStringArray(provider...)},
	})

	switch {
	case s.replaceUpstream && replaced:
		return fmt.Errorf("%s already replaces crates.io with %q; remove that source replacement and rerun this command", path, replacement)
	case s.replaceUpstream:
		doc.appendTable("source.crates-io", [][2]string{{"replace-with", tomlString(cargoRegistryName)}})
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
	doc.removeTable(registryTable)

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
