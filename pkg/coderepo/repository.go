package coderepo

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
)

type Repository struct {
	RepositoryID  string `json:"repositoryId"`
	OrgID         string `json:"orgId"`
	Name          string `json:"name"`
	DefaultBranch string `json:"defaultBranch"`
	CloneURL      string `json:"cloneUrl"`
	WebURL        string `json:"webUrl"`
}

var identityPattern = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

func Describe(org string, repo *code.Repository) (Repository, error) {
	if !identityPattern.MatchString(repo.GetRepositoryId()) || !identityPattern.MatchString(org) || repo.GetName() == "" || strings.ContainsFunc(repo.GetDefaultBranch(), unicode.IsControl) {
		return Repository{}, fmt.Errorf("repository lookup returned an invalid identity")
	}
	for _, part := range strings.Split(repo.Name, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\") || strings.ContainsFunc(part, unicode.IsControl) {
			return Repository{}, fmt.Errorf("repository lookup returned an invalid name")
		}
	}
	return Repository{repo.RepositoryId, org, repo.Name, repo.DefaultBranch,
		(&url.URL{Scheme: "https", Host: org + ".code.depot.dev", Path: "/" + repo.Name}).String(),
		(&url.URL{Scheme: "https", Host: "depot.dev", Path: "/orgs/" + org + "/code/repositories/" + repo.RepositoryId}).String()}, nil
}

func Lookup(ctx context.Context, org, token, id, name string) (Repository, error) {
	req := &code.GetRepositoryRequest{Lookup: &code.GetRepositoryRequest_Name{Name: name}}
	if id != "" {
		req.Lookup = &code.GetRepositoryRequest_RepositoryId{RepositoryId: id}
	}
	res, err := api.NewCodeClient().GetRepository(ctx, request(token, org, req))
	if err != nil {
		return Repository{}, repositoryLookupError("repository lookup", err)
	}
	if id != "" && res.Msg.GetRepository().GetRepositoryId() != id {
		return Repository{}, fmt.Errorf("repository lookup returned a different repository")
	}
	return Describe(org, res.Msg.Repository)
}
