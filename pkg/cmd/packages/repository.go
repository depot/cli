package packages

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const defaultPackagesURL = "https://packages.depot.dev"

var (
	orgIDPattern          = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	repositoryNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9]|-[a-z0-9]){0,63}$`)
)

type repository struct {
	server *url.URL
	org    string
	format string
	name   string
}

func newRepository(org, format, name string) (repository, error) {
	if !orgIDPattern.MatchString(org) {
		return repository{}, fmt.Errorf("invalid organization ID %q", org)
	}
	if !repositoryNamePattern.MatchString(name) || len(name) > 64 {
		return repository{}, fmt.Errorf("invalid repository name %q: use up to 64 lowercase letters, digits, and single hyphens", name)
	}

	server, err := packagesServer()
	if err != nil {
		return repository{}, err
	}

	return repository{server: server, org: org, format: format, name: name}, nil
}

func packagesServer() (*url.URL, error) {
	raw := os.Getenv("DEPOT_PACKAGES_URL")
	if raw == "" {
		raw = defaultPackagesURL
	}

	server, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil || server.Host == "" || (server.Scheme != "https" && server.Scheme != "http") ||
		server.User != nil || server.RawQuery != "" || server.ForceQuery || server.Fragment != "" {
		return nil, fmt.Errorf("invalid DEPOT_PACKAGES_URL %q", raw)
	}
	return server, nil
}

func (r repository) host() string {
	return r.server.Host
}

func (r repository) hostname() string {
	return r.server.Hostname()
}

func (r repository) formatURL() string {
	return fmt.Sprintf("%s/%s/%s/", r.server.String(), r.org, r.format)
}

func (r repository) url() string {
	return r.formatURL() + r.name + "/"
}

func (r repository) urlWithCredentials(username, password string) string {
	u := *r.server
	u.User = url.UserPassword(username, password)
	return fmt.Sprintf("%s/%s/%s/%s/", u.String(), r.org, r.format, r.name)
}

// nerfDart returns the URL without its scheme, as used by npm-style
// configuration keys such as "//host/path/:_authToken".
func (r repository) nerfDart() string {
	return "//" + r.host() + r.server.EscapedPath() + "/" + r.org + "/" + r.format + "/" + r.name + "/"
}
