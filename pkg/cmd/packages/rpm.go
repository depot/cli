package packages

import (
	"fmt"
	"os"
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

type rpmRepository struct {
	id          string
	baseURL     string
	metadataKey string
	packageKey  string
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

	packageKey := s.opts.packageKey
	switch {
	case packageKey == "" && slices.Contains(rpmBuiltinCaches, s.repo.name):
		return rpmRepository{}, fmt.Errorf("specify the distribution's package signing key with --package-key, such as --package-key /etc/pki/rpm-gpg/RPM-GPG-KEY-Rocky-9")
	case packageKey == "":
		s.notef("Depot verifies repository metadata but doesn't sign individual packages, so package signature checks are off. Pass --package-key with your publisher's key file to turn them on.")
		packageKey = packageKeyNone
	case packageKey == packageKeyNone:
		s.notef("Package signature checks are off because --package-key is none.")
	default:
		if _, err := os.Stat(packageKey); err != nil {
			return rpmRepository{}, fmt.Errorf("package key %s: %w", packageKey, err)
		}
	}

	key, err := s.download(s.repo.url() + "signing-key.asc")
	if err != nil {
		return rpmRepository{}, err
	}
	id := s.repo.fileID() + "-" + strings.Trim(nonIDCharacters.ReplaceAllString(strings.ToLower(path), "-"), "-")
	metadataKey := "/etc/pki/rpm-gpg/RPM-GPG-KEY-" + s.repo.fileID()
	if err := s.writeSystemFile(metadataKey, key, 0644); err != nil {
		return rpmRepository{}, err
	}

	return rpmRepository{
		id:          id,
		baseURL:     s.repo.url() + path + "/",
		metadataKey: metadataKey,
		packageKey:  packageKey,
	}, nil
}

func (r rpmRepository) gpgSettings() (packageCheck string, keys string) {
	keys = "file://" + r.metadataKey
	if r.packageKey == packageKeyNone {
		return "0", keys
	}
	return "1", keys + " file://" + r.packageKey
}

func configureYumRepo(s *setup) error {
	repo, err := prepareRPMRepository(s)
	if err != nil {
		return err
	}

	packageCheck, keys := repo.gpgSettings()
	content := fmt.Sprintf(`[%s]
name=Depot %s
baseurl=%s
username=depot
password=%s
enabled=1
repo_gpgcheck=1
gpgcheck=%s
gpgkey=%s
metadata_expire=60
`, repo.id, repo.id, repo.baseURL, s.token, packageCheck, keys)

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

	packageCheck, keys := repo.gpgSettings()
	content := fmt.Sprintf(`[%s]
name=Depot %s
baseurl=%s?credentials=%s
enabled=1
autorefresh=1
gpgcheck=1
repo_gpgcheck=1
pkg_gpgcheck=%s
gpgkey=%s
`, repo.id, repo.id, repo.baseURL, repo.id, packageCheck, keys)
	if err := s.writeSystemFile("/etc/zypp/repos.d/"+repo.id+".repo", []byte(content), 0644); err != nil {
		return err
	}

	keyFiles := []string{repo.metadataKey}
	if repo.packageKey != packageKeyNone {
		keyFiles = append(keyFiles, repo.packageKey)
	}
	for _, key := range keyFiles {
		if _, err := s.runPrivileged(nil, "rpm", "--import", key); err != nil {
			return err
		}
	}
	return nil
}
