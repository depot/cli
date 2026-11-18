package ci

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"connectrpc.com/connect"
	"github.com/charmbracelet/colorprofile"
	"github.com/depot/cli/pkg/api"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

// Entire's API does not identify a repository's GitHub upstream, so a secret migration intent cannot be bound to it.
var errEntireSecretMigration = errors.New("Depot cannot read secrets and variables from Entire. " +
	"Run `depot ci migrate secrets-and-vars` from a checkout whose push remote is the GitHub repository that holds them, " +
	"or add them with `depot ci secrets add` and `depot ci vars add`")

type entireRepositoryAccessClient interface {
	GetEntireRepositoryAccess(context.Context, *connect.Request[civ1.GetEntireRepositoryAccessRequest]) (*connect.Response[civ1.GetEntireRepositoryAccessResponse], error)
}

// entirePreflight checks that one of the org's Entire installations grants Depot access to the
// checkout's Entire repository. Like githubPreflight, it returns a nil result after printing a
// user-facing message when the check fails.
func entirePreflight(ctx context.Context, opts migrateOptions) (*preflightResult, error) {
	token, orgID, err := resolveAuth(ctx, opts)
	if err != nil {
		return nil, err
	}
	repo, err := detectEntireRepository(opts.dir)
	if err != nil {
		return nil, err
	}

	client := opts.entireAccessClient
	if client == nil {
		client = api.NewMigrationClient()
	}
	resp, err := client.GetEntireRepositoryAccess(ctx, api.WithAuthenticationAndOrg(
		connect.NewRequest(&civ1.GetEntireRepositoryAccessRequest{Repo: repo}),
		token, orgID,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to check Entire access for %s: %w", repo, err)
	}

	out := opts.stdout
	if out == nil {
		out = os.Stdout
	}
	out = colorprofile.NewWriter(out, os.Environ())
	bold := lipgloss.NewStyle().Bold(true)
	fmt.Fprintf(out, "\nDetected repository: %s\n", bold.Render(repo))

	if !resp.Msg.GetInstalled() {
		slug := orgID
		if slug == "" {
			slug = "_"
		}
		fmt.Fprintf(out, "Entire is not connected to this Depot organization.\n\n")
		fmt.Fprintf(out, "Connect it at: https://depot.dev/orgs/%s/settings/ci/general\n", slug)
		return nil, nil
	}
	if !resp.Msg.GetRepoAccessible() {
		fmt.Fprintf(out, "Depot's Entire installation does not have access to %s.\n\n", bold.Render(repo))
		fmt.Fprintln(out, "Add the repository to the Depot plugin installation in Entire.")
		return nil, nil
	}

	fmt.Fprintf(out, "Entire repository is available to this Depot organization.\n\n")
	return &preflightResult{token: token, orgID: orgID, repo: repo}, nil
}

func detectEntireRepository(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	_, detected := detectRunRepositories(dir)
	var repos []string
	for _, repository := range detected {
		if repository.forge == civ1.Forge_FORGE_ENTIRE {
			repos = append(repos, repository.repo)
		}
	}
	switch len(repos) {
	case 0:
		return "", fmt.Errorf("could not detect an Entire repository from git remotes — configure a remote pointing to an Entire repository")
	case 1:
		return repos[0], nil
	default:
		return "", fmt.Errorf("multiple Entire repositories found in git remotes: %s", strings.Join(repos, ", "))
	}
}
