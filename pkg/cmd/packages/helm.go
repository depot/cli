package packages

import (
	"fmt"
	"os/exec"
	"strings"
)

const helmRepositoryName = "depot"

func configureHelm(s *setup) error {
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
