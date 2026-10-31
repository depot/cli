package packages

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

const helmRepositoryName = "depot"

func configureHelm(s *setup) error {
	out, err := exec.CommandContext(s.ctx, "helm", "env", "HELM_REPOSITORY_CONFIG").Output()
	if err != nil {
		return fmt.Errorf("could not find the Helm repository configuration: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if info, err := os.Stat(resolveLink(path)); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("the Helm repository configuration %s isn't a regular file: set HELM_REPOSITORY_CONFIG to a file, then rerun this command", path)
	}
	if err := checkHelmAlias(s); err != nil {
		return err
	}

	args := []string{"repo", "add", helmRepositoryName, s.repo.url(), "--username", "depot", "--password-stdin", "--force-update"}
	if helmPassesCredentials(s, path) {
		args = append(args, "--pass-credentials")
	}
	if err := s.runQuietWithInput([]byte(s.token), "helm", args...); err != nil {
		return err
	}
	if err := restrictPermissions(path); err != nil {
		return err
	}
	s.changedFile(path)

	s.notef("Install charts from the %q repository, for example:\n  helm install widget %s/widget", helmRepositoryName, helmRepositoryName)
	return nil
}

// checkHelmAlias refuses to replace a depot repository alias that points
// outside Depot Packages, because --force-update overwrites it.
func checkHelmAlias(s *setup) error {
	out, err := exec.CommandContext(s.ctx, "helm", "repo", "list", "-o", "json").Output()
	if err != nil {
		return nil
	}
	var repositories []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(out, &repositories); err != nil {
		return nil
	}
	for _, repository := range repositories {
		if repository.Name != helmRepositoryName || strings.TrimSuffix(repository.URL, "/") == strings.TrimSuffix(s.repo.url(), "/") {
			continue
		}
		if !s.repo.isServerRepositoryURL(repository.URL, "helm") {
			return fmt.Errorf("the Helm repository %q already points to %s: rename or remove it with `helm repo remove %s`, then rerun this command", helmRepositoryName, repository.URL, helmRepositoryName)
		}
		s.notef("The %q Helm repository now points to %s instead of %s.", helmRepositoryName, s.repo.url(), repository.URL)
	}
	return nil
}

// helmPassesCredentials reports whether the existing depot entry, which
// checkHelmAlias allows only for a Depot Packages repository,
// sends credentials to all domains, so that a token refresh keeps that
// setting.
func helmPassesCredentials(s *setup, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var config struct {
		Repositories []struct {
			Name               string `yaml:"name"`
			URL                string `yaml:"url"`
			PassCredentialsAll bool   `yaml:"pass_credentials_all"`
		} `yaml:"repositories"`
	}
	if yaml.Unmarshal(data, &config) != nil {
		return false
	}
	for _, repository := range config.Repositories {
		if repository.Name == helmRepositoryName && s.repo.isServerRepositoryURL(repository.URL, "helm") {
			return repository.PassCredentialsAll
		}
	}
	return false
}
