package packages

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const packageKeyNone = "none"

var (
	rpmBuiltinCaches = []string{"fedora", "epel", "rocky", "almalinux", "opensuse"}
	rpmPathPattern   = regexp.MustCompile(`^[A-Za-z0-9._$-]+(/[A-Za-z0-9._$-]+)*$`)
	nonIDCharacters  = regexp.MustCompile(`[^a-z0-9]+`)
)

// rpmRepository verifies either Depot's metadata signature or the publisher's
// package signatures, never both with one key list. dnf and zypper trust every
// listed key for packages, so listing Depot's metadata key next to a package
// key would let packages signed by Depot pass the package check.
type rpmRepository struct {
	id           string
	baseURL      string
	key          string
	repoCheck    string
	packageCheck string
}

func prepareRPMRepository(s *setup) (rpmRepository, error) {
	if err := s.requireSystemClient(); err != nil {
		return rpmRepository{}, err
	}

	path := strings.Trim(s.opts.path, "/")
	if path == "" {
		return rpmRepository{}, fmt.Errorf("specify the distribution path in the repository with --path, such as --path el/9")
	}
	if !rpmPathPattern.MatchString(path) || slices.Contains(strings.Split(path, "/"), "..") {
		return rpmRepository{}, fmt.Errorf("invalid --path %q", s.opts.path)
	}

	repo := rpmRepository{
		id:      s.repo.fileID() + "-" + strings.Trim(nonIDCharacters.ReplaceAllString(strings.ToLower(path), "-"), "-"),
		baseURL: s.repo.url() + path + "/",
	}

	packageKey := s.opts.packageKey
	switch {
	case packageKey == "" && slices.Contains(rpmBuiltinCaches, s.repo.name):
		return rpmRepository{}, fmt.Errorf("specify the distribution's package signing key with --package-key, such as --package-key /etc/pki/rpm-gpg/RPM-GPG-KEY-Rocky-9")
	case packageKey == "" || packageKey == packageKeyNone:
		if packageKey == "" {
			s.notef("Depot verifies repository metadata but doesn't sign individual packages, so package signature checks are off. Pass --package-key with your publisher's key file to verify packages instead.")
		} else {
			s.notef("Package signature checks are off because --package-key is none.")
		}
		key, err := s.download(s.repo.url() + "signing-key.asc")
		if err != nil {
			return rpmRepository{}, err
		}
		repo.key = "/etc/pki/rpm-gpg/RPM-GPG-KEY-" + s.repo.fileID()
		if err := s.writeSystemFile(repo.key, key, 0644); err != nil {
			return rpmRepository{}, err
		}
		repo.repoCheck, repo.packageCheck = "1", "0"
	default:
		absolute, err := filepath.Abs(packageKey)
		if err != nil {
			return rpmRepository{}, err
		}
		if _, err := os.Stat(absolute); err != nil {
			return rpmRepository{}, fmt.Errorf("package key %s: %w", packageKey, err)
		}
		packageKey = absolute
		s.notef("Packages are verified with %s. Repository metadata signatures aren't checked, because the client would also trust Depot's metadata key for packages.", packageKey)
		repo.key = packageKey
		repo.repoCheck, repo.packageCheck = "0", "1"
	}

	return repo, nil
}

func configureYumRepo(s *setup) error {
	repo, err := prepareRPMRepository(s)
	if err != nil {
		return err
	}

	content := fmt.Sprintf(`[%s]
name=Depot %s
baseurl=%s
username=depot
password=%s
enabled=1
repo_gpgcheck=%s
gpgcheck=%s
gpgkey=%s
metadata_expire=60
`, repo.id, repo.id, repo.baseURL, s.token, repo.repoCheck, repo.packageCheck, fileURL(repo.key))

	return s.writeSystemFile("/etc/yum.repos.d/"+repo.id+".repo", []byte(content), 0600)
}

func configureZypper(s *setup) error {
	repo, err := prepareRPMRepository(s)
	if err != nil {
		return err
	}

	credentials := fmt.Sprintf("username=depot\npassword=%s\n", s.token)
	if err := s.writeSystemFile("/etc/zypp/credentials.d/"+repo.id, []byte(credentials), 0600); err != nil {
		return err
	}

	content := fmt.Sprintf(`[%s]
name=Depot %s
baseurl=%s?credentials=%s
enabled=1
autorefresh=1
gpgcheck=1
repo_gpgcheck=%s
pkg_gpgcheck=%s
gpgkey=%s
`, repo.id, repo.id, repo.baseURL, repo.id, repo.repoCheck, repo.packageCheck, fileURL(repo.key))
	if err := s.writeSystemFile("/etc/zypp/repos.d/"+repo.id+".repo", []byte(content), 0644); err != nil {
		return err
	}

	// Keys in RPM's database verify packages, so setup imports only a package
	// key. zypper asks the user to trust Depot's metadata key on refresh.
	if repo.packageCheck == "1" {
		if _, err := s.runPrivileged(nil, "rpm", "--import", repo.key); err != nil {
			return err
		}
	} else {
		s.notef("Run `sudo zypper refresh %s` and confirm the repository key when zypper asks.", repo.id)
	}
	return nil
}

func fileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}
