package packages

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

const (
	signingKeyRepository = "repository"
	signingKeyUpstream   = "upstream"
)

var aptBuiltinCaches = []string{"debian", "debian-security", "ubuntu", "ubuntu-ports"}

func configureApt(s *setup) error {
	if err := s.requireSystemClient(); err != nil {
		return err
	}

	release := osRelease()
	suite := s.opts.suite
	if suite == "" {
		suite = defaultAptSuite(s.repo.name, release["VERSION_CODENAME"])
	}
	if suite == "" {
		return fmt.Errorf("could not detect the distribution suite: specify --suite, such as --suite bookworm")
	}

	components := s.opts.components
	if len(components) == 0 {
		components = []string{"main"}
	}

	signingKey := s.opts.signingKey
	if signingKey == "" {
		signingKey = signingKeyRepository
		if slices.Contains(aptBuiltinCaches, s.repo.name) {
			signingKey = signingKeyUpstream
		}
	}

	var keyring string
	switch signingKey {
	case signingKeyUpstream:
		var err error
		keyring, err = aptUpstreamKeyring(s.repo.name, release["ID"])
		if err != nil {
			return err
		}
	case signingKeyRepository:
		key, err := s.download(s.repo.url() + "signing-key.asc")
		if err != nil {
			return err
		}
		keyring = "/etc/apt/keyrings/" + s.repo.fileID() + ".asc"
		if err := s.writeSystemFile(keyring, key, 0644); err != nil {
			return err
		}
	default:
		return fmt.Errorf(`invalid --signing-key %q: use "repository" or "upstream"`, signingKey)
	}

	auth := fmt.Sprintf("machine %s\nlogin depot\npassword %s\n", s.repo.authMachine(s.repo.resourcePath()), s.token)
	if err := s.writeSystemFile("/etc/apt/auth.conf.d/"+s.repo.fileID()+".conf", []byte(auth), 0600); err != nil {
		return err
	}

	sources := fmt.Sprintf("Types: deb\nURIs: %s\nSuites: %s\nComponents: %s\nSigned-By: %s\n",
		s.repo.url(), suite, strings.Join(components, " "), keyring)
	if err := s.writeSystemFile("/etc/apt/sources.list.d/"+s.repo.fileID()+".sources", []byte(sources), 0644); err != nil {
		return err
	}

	s.notef("Run `sudo apt-get update` to load the repository. Existing APT sources are unchanged; disable direct upstream entries if all downloads should use Depot.")
	return nil
}

// defaultAptSuite adds the -security suffix for Debian's security archive,
// which publishes each release under <codename>-security.
func defaultAptSuite(repositoryName, codename string) string {
	if codename != "" && repositoryName == "debian-security" {
		return codename + "-security"
	}
	return codename
}

func aptUpstreamKeyring(repositoryName, distribution string) (string, error) {
	vendor := distribution
	switch {
	case strings.HasPrefix(repositoryName, "debian"):
		vendor = "debian"
	case strings.HasPrefix(repositoryName, "ubuntu"):
		vendor = "ubuntu"
	}

	if vendor == "" {
		return "", fmt.Errorf("could not determine the distribution keyring: use --signing-key repository")
	}
	keyring := "/usr/share/keyrings/" + vendor + "-archive-keyring.gpg"
	if _, err := os.Stat(keyring); err != nil {
		return "", fmt.Errorf("the %s keyring %s isn't installed: install the %s-archive-keyring package or use --signing-key repository", vendor, keyring, vendor)
	}
	return keyring, nil
}
