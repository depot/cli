package coderepo

import (
	"fmt"

	"github.com/depot/cli/pkg/api"
	gitv1 "github.com/depot/cli/pkg/proto/depot/git/v1"
	"github.com/spf13/cobra"
)

// CheckoutRemote verifies a known repository and returns its configured Depot URL, or empty for a GitHub-only mirror.
func CheckoutRemote(cmd *cobra.Command, org, token, repositoryID string) (string, error) {
	if org == "" || repositoryID == "" {
		return "", fmt.Errorf("checkout requires a repository ID and organization; check --org")
	}
	urls, err := remoteURLs(cmd)
	if err != nil {
		return "", fmt.Errorf("cannot read Git remotes from this checkout")
	}
	client := api.NewGitRepositoryClient()
	fetchURL := ""
	var github []string
	for _, raw := range urls {
		remote, ok := ParseRemote(raw)
		if !ok {
			continue
		}
		if remote.Org != "" && remote.Org != org {
			return "", fmt.Errorf("a Depot remote belongs to a different organization; check --org")
		}
		if remote.GitHub {
			github = append(github, remote.Name)
			continue
		}
		res, err := client.Resolve(cmd.Context(), request(token, org, &gitv1.ResolveRequest{
			Repository: &gitv1.RepositorySelector{Value: &gitv1.RepositorySelector_Name{Name: remote.Name}},
		}))
		if err != nil {
			return "", repositoryLookupError("checkout repository lookup", err)
		}
		if res.Msg.GetRepository().GetId() != repositoryID || res.Msg.GetRepository().GetOrgId() != org {
			return "", fmt.Errorf("local checkout does not match the selected repository")
		}
		if fetchURL == "" {
			fetchURL = raw
		}
	}
	if fetchURL != "" {
		return fetchURL, nil
	}
	if len(github) > 0 {
		res, err := client.Resolve(cmd.Context(), request(token, org, &gitv1.ResolveRequest{
			Repository: &gitv1.RepositorySelector{Value: &gitv1.RepositorySelector_Id{Id: repositoryID}},
		}))
		if err != nil {
			return "", repositoryLookupError("checkout repository lookup", err)
		}
		repo := res.Msg.GetRepository()
		if repo.GetId() == repositoryID && repo.GetOrgId() == org {
			for _, name := range github {
				if matchesUpstream(repo, name) {
					return "", nil
				}
			}
		}
	}
	return "", fmt.Errorf("local checkout does not match the selected repository")
}
