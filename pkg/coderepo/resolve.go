package coderepo

import (
	"fmt"
	"os/exec"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/gitremote"
	gitv1 "github.com/depot/cli/pkg/proto/depot/git/v1"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

type Remote struct {
	Name, Org string
	GitHub    bool
}

func ParseRemote(raw string) (Remote, bool) {
	// A local path containing a colon or @ is not an scp-style remote.
	if !strings.Contains(raw, "://") {
		prefix, _, _ := strings.Cut(raw, ":")
		if strings.ContainsAny(prefix, "/\\") {
			return Remote{}, false
		}
	}
	host, path, ok := gitremote.SplitURL(raw)
	if !ok {
		return Remote{}, false
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	path = strings.Trim(path, "/")
	if path == "" || strings.ContainsAny(path, "?#\\\r\n\t") {
		return Remote{}, false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return Remote{}, false
		}
	}
	if host == "github.com" {
		if strings.HasSuffix(strings.ToLower(path), ".git") {
			path = path[:len(path)-4]
		}
		parts := strings.Split(path, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return Remote{}, false
		}
		return Remote{Name: strings.ToLower(path), GitHub: true}, true
	}
	if host == "code.depot.dev" {
		return Remote{Name: path}, true
	}
	if org, ok := strings.CutSuffix(host, ".code.depot.dev"); ok && org != "" && !strings.Contains(org, ".") {
		return Remote{Name: path, Org: org}, true
	}
	return Remote{}, false
}

func repositoryInferenceError(reason string) error {
	return fmt.Errorf("cannot infer Depot repository: %s; use --repo for MR commands or --id for repository commands with the Depot repository ID", reason)
}

func repositoryLookupError(operation string, err error) error {
	// Do not echo server messages or Git URLs: either can contain credentials.
	return repositoryInferenceError(fmt.Sprintf("%s failed (%s); check --org and repository access", operation, connect.CodeOf(err)))
}

func Infer(cmd *cobra.Command, org, token, remoteURL string) (string, error) {
	if org == "" {
		return "", repositoryInferenceError("select an organization with --org or `depot org switch`")
	}
	var urls []string
	if remoteURL != "" {
		urls = []string{remoteURL}
	} else {
		names, err := git(cmd, "remote")
		if err != nil {
			return "", repositoryInferenceError("cannot read Git remotes from this checkout")
		}
		for _, name := range strings.Fields(names) {
			raw, err := git(cmd, "remote", "get-url", "--all", "--", name)
			if err == nil {
				urls = append(urls, strings.Split(raw, "\n")...)
			}
		}
	}
	var remotes []Remote
	seen := make(map[Remote]bool)
	for _, raw := range urls {
		remote, ok := ParseRemote(raw)
		if !ok || seen[remote] {
			continue
		}
		if remote.Org != "" && remote.Org != org {
			return "", repositoryInferenceError("a Depot remote belongs to a different organization; check --org")
		}
		seen[remote] = true
		remotes = append(remotes, remote)
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
		if repo.OrgId != org {
			return repositoryInferenceError("resolved repository does not belong to the selected organization; check --org")
		}
		if target != nil && target.Id != repo.Id {
			return repositoryInferenceError("multiple repositories match the Git remotes")
		}
		target = repo
		return nil
	}
	for _, remote := range remotes {
		if remote.GitHub {
			continue
		}
		resp, err := client.Resolve(cmd.Context(), request(token, org, &gitv1.ResolveRequest{
			Repository: &gitv1.RepositorySelector{Value: &gitv1.RepositorySelector_Name{Name: remote.Name}},
		}))
		if err != nil {
			return "", repositoryLookupError("repository lookup", err)
		}
		if err := selectTarget(resp.Msg.Repository); err != nil {
			return "", err
		}
	}

	depotTarget := target
	// A Depot remote that resolves to this upstream already identifies the
	// mirror, including when its canonical name differs from its GitHub name.
	var unmatched []string
	for _, remote := range remotes {
		if remote.GitHub && !matchesUpstream(target, remote.Name) {
			unmatched = append(unmatched, remote.Name)
		}
	}
	if len(unmatched) > 0 {
		matched := make(map[string]bool)
		pageToken := ""
		seenPages := make(map[string]bool)
		for {
			resp, err := client.List(cmd.Context(), request(token, org, &gitv1.ListRequest{
				PageSize: proto.Int32(500), PageToken: proto.String(pageToken),
			}))
			if err != nil {
				// Listing requires owner access. A directly resolved Depot remote
				// remains usable by members without permission to discover mirrors.
				if depotTarget != nil && connect.CodeOf(err) == connect.CodePermissionDenied {
					return depotTarget.Id, nil
				}
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
		// Extra GitHub remotes may be personal forks without a Depot mirror.
		// They do not invalidate a target another remote resolved.
		if depotTarget == nil && len(matched) == 0 {
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

func request[T any](token, org string, message *T) *connect.Request[T] {
	return api.WithAuthenticationAndOrg(connect.NewRequest(message), token, org)
}
func git(cmd *cobra.Command, args ...string) (string, error) {
	out, err := exec.CommandContext(cmd.Context(), "git", args...).Output()
	return strings.TrimSpace(string(out)), err
}
