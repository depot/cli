package packages

const helmRepositoryName = "depot"

func configureHelm(s *setup) error {
	err := s.runQuietWithInput([]byte(s.token), "helm", "repo", "add", helmRepositoryName, s.repo.url(),
		"--username", "depot", "--password-stdin", "--force-update")
	if err != nil {
		return err
	}
	s.changes = append(s.changes, "Helm repository configuration")
	s.notef("Install charts from the %q repository, for example:\n  helm install widget %s/widget", helmRepositoryName, helmRepositoryName)
	return nil
}
