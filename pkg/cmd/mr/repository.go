package mr

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/gitremote"
	gitv1 "github.com/depot/cli/pkg/proto/depot/git/v1"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

type repositoryRemote struct {
	name, org string
	github    bool
}

func parseRepositoryRemote(raw string) (repositoryRemote, bool) {
	// A local path containing a colon or @ is not an scp-style remote.
	if !strings.Contains(raw, "://") {
		prefix, _, _ := strings.Cut(raw, ":")
		if strings.ContainsAny(prefix, "/\\") {
			return repositoryRemote{}, false
		}
	}
	host, path, ok := gitremote.SplitURL(raw)
	if !ok {
		return repositoryRemote{}, false
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	path = strings.Trim(path, "/")
	if path == "" || strings.ContainsAny(path, "?#\\\r\n\t") {
		return repositoryRemote{}, false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return repositoryRemote{}, false
		}
	}
	if host == "github.com" {
		if strings.HasSuffix(strings.ToLower(path), ".git") {
			path = path[:len(path)-4]
		}
		parts := strings.Split(path, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return repositoryRemote{}, false
		}
		return repositoryRemote{name: strings.ToLower(path), github: true}, true
	}
	if host == "code.depot.dev" {
		return repositoryRemote{name: path}, true
	}
	if org, ok := strings.CutSuffix(host, ".code.depot.dev"); ok && org != "" && !strings.Contains(org, ".") {
		return repositoryRemote{name: path, org: org}, true
	}
	return repositoryRemote{}, false
}

func repositoryInferenceError(reason string) error {
	return fmt.Errorf("cannot infer Depot repository: %s; use --repo with the Depot repository ID", reason)
}

func repositoryLookupError(operation string, err error) error {
	// Do not echo server messages or Git URLs: either can contain credentials.
	return repositoryInferenceError(fmt.Sprintf("%s failed (%s); check --org and repository access", operation, connect.CodeOf(err)))
}

func (o *options) inferRepository(cmd *cobra.Command) (string, error) {
	if o.org == "" {
		return "", repositoryInferenceError("select an organization with --org or `depot org switch`")
	}
	names, err := git(cmd, "remote")
	if err != nil {
		return "", repositoryInferenceError("cannot read Git remotes from this checkout")
	}
	var remotes []repositoryRemote
	seen := make(map[repositoryRemote]bool)
	for _, name := range strings.Fields(names) {
		urls, err := git(cmd, "remote", "get-url", "--all", name)
		if err != nil {
			return "", repositoryInferenceError("cannot read Git remote URLs")
		}
		for _, raw := range strings.Split(urls, "\n") {
			remote, ok := parseRepositoryRemote(raw)
			if !ok || seen[remote] {
				continue
			}
			if remote.org != "" && remote.org != o.org {
				return "", repositoryInferenceError("a Depot remote belongs to a different organization; check --org")
			}
			seen[remote] = true
			remotes = append(remotes, remote)
		}
	}
	if len(remotes) == 0 {
		return "", repositoryInferenceError("no supported Git remotes found")
	}

	client := api.NewGitRepositoryClient()
	var target *gitv1.Repository
	selectTarget := func(repo *gitv1.Repository) error {
		if repo.GetId() == "" {
			return repositoryInferenceError("no matching repository in the selected organization")
		}
		if repo.OrgId != o.org {
			return repositoryInferenceError("resolved repository does not belong to the selected organization; check --org")
		}
		if target != nil && target.Id != repo.Id {
			return repositoryInferenceError("multiple repositories match the Git remotes")
		}
		target = repo
		return nil
	}
	for _, remote := range remotes {
		if remote.github {
			continue
		}
		resp, err := client.Resolve(cmd.Context(), request(o, &gitv1.ResolveRequest{
			Repository: &gitv1.RepositorySelector{Value: &gitv1.RepositorySelector_Name{Name: remote.name}},
		}))
		if err != nil {
			return "", repositoryLookupError("repository lookup", err)
		}
		if err := selectTarget(resp.Msg.Repository); err != nil {
			return "", err
		}
	}

	// A Depot remote that resolves to this upstream already identifies the
	// mirror, including when its canonical name differs from its GitHub name.
	var unmatched []string
	for _, remote := range remotes {
		if remote.github && !matchesUpstream(target, remote.name) {
			unmatched = append(unmatched, remote.name)
		}
	}
	if len(unmatched) > 0 {
		matched := make(map[string]bool)
		pageToken := ""
		seenPages := make(map[string]bool)
		for {
			resp, err := client.List(cmd.Context(), request(o, &gitv1.ListRequest{
				PageSize: proto.Int32(500), PageToken: proto.String(pageToken),
			}))
			if err != nil {
				return "", repositoryLookupError("existing mirror lookup", err)
			}
			for _, repo := range resp.Msg.Repositories {
				for _, name := range unmatched {
					if matchesUpstream(repo, name) {
						if err := selectTarget(repo); err != nil {
							return "", err
						}
						matched[name] = true
					}
				}
			}
			pageToken = resp.Msg.GetNextPageToken()
			if pageToken == "" {
				break
			}
			if seenPages[pageToken] {
				return "", repositoryInferenceError("mirror listing returned a repeated page token")
			}
			seenPages[pageToken] = true
		}
		if len(matched) != len(unmatched) {
			return "", repositoryInferenceError("no existing Depot mirror matches a GitHub remote in the selected organization")
		}
	}
	return target.Id, nil
}

func matchesUpstream(repo *gitv1.Repository, name string) bool {
	upstream := repo.GetUpstream()
	return upstream.GetProvider() == gitv1.UpstreamProvider_UPSTREAM_PROVIDER_GITHUB &&
		strings.EqualFold(upstream.GetOwner()+"/"+upstream.GetName(), name)
}
