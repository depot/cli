package packages

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

const helmRepositoryName = "depot"

func configureHelm(s *setup) error {
	if err := checkHelmAlias(s); err != nil {
		return err
	}

	err := s.runQuietWithInput([]byte(s.token), "helm", "repo", "add", helmRepositoryName, s.repo.url(),
		"--username", "depot", "--password-stdin", "--force-update")
	if err != nil {
		return err
	}

	out, err := exec.CommandContext(s.ctx, "helm", "env", "HELM_REPOSITORY_CONFIG").Output()
	if err != nil {
		return fmt.Errorf("could not find the Helm repository configuration: %w", err)
	}
	path := strings.TrimSpace(string(out))
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
		if !strings.HasPrefix(repository.URL, s.repo.formatURL()) {
			return fmt.Errorf("the Helm repository %q already points to %s: rename or remove it with `helm repo remove %s`, then rerun this command", helmRepositoryName, repository.URL, helmRepositoryName)
		}
		s.notef("The %q Helm repository now points to %s instead of %s.", helmRepositoryName, s.repo.url(), repository.URL)
	}
	return nil
}
