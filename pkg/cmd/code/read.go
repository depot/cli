package code

import (
	"encoding/json"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/coderepo"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

type readOptions struct{ org, token, output string }

func (o *readOptions) flags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.org, "org", "", "Organization ID (defaults to the current organization)")
	cmd.Flags().StringVar(&o.token, "token", "", "Depot API token")
	cmd.Flags().StringVarP(&o.output, "output", "o", "", "Output format (json)")
}
func (o *readOptions) authenticate(cmd *cobra.Command) error {
	if o.output != "" && o.output != "json" {
		return fmt.Errorf("unsupported output format: use json")
	}
	if !cmd.Flags().Changed("org") {
		o.org = config.GetCurrentOrganization()
	}
	if strings.TrimSpace(o.org) == "" {
		return fmt.Errorf("organization is required: use --org or `depot org switch`")
	}
	if helpers.OnlyATurnToken(o.token) {
		return fmt.Errorf("repository commands require a user credential; use the session's Depot Code tools")
	}
	token, err := helpers.ResolveOrgAuth(cmd.Context(), o.token)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}
	o.token = token
	return nil
}

func newView() *cobra.Command {
	o := &readOptions{}
	var id, remote string
	cmd := &cobra.Command{Use: "view [name]", Short: "Look up a repository by name, alias, ID or local Git remotes", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			selectors := len(args)
			if cmd.Flags().Changed("id") {
				selectors++
				if strings.TrimSpace(id) == "" {
					return fmt.Errorf("--id must not be blank")
				}
			}
			if cmd.Flags().Changed("remote") {
				selectors++
				if strings.TrimSpace(remote) == "" {
					return fmt.Errorf("--remote must not be blank")
				}
			}
			if selectors > 1 {
				return fmt.Errorf("pass a name, --id or --remote, not more than one")
			}
			if len(args) > 0 && strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("repository name must not be blank")
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			if id == "" && name == "" {
				var err error
				id, err = coderepo.Infer(cmd, o.org, o.token, remote)
				if err != nil {
					return err
				}
			}
			repo, err := coderepo.Lookup(cmd.Context(), o.org, o.token, id, name)
			if err != nil {
				return err
			}
			if o.output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(repo)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\nDefault branch: %s\nClone URL: %s\nWeb URL: %s\n", repo.Name, repo.RepositoryID, repo.DefaultBranch, repo.CloneURL, repo.WebURL)
			return err
		},
	}
	o.flags(cmd)
	cmd.Flags().StringVar(&id, "id", "", "Depot repository ID, instead of a name")
	cmd.Flags().StringVar(&remote, "remote", "", "Resolve this Git remote URL instead of reading local remotes")
	return cmd
}

func newList() *cobra.Command {
	o := &readOptions{}
	var limit uint32
	var page string
	cmd := &cobra.Command{Use: "list", Short: "List repositories in an organization", Args: cobra.NoArgs,
		Long: "List one page of repositories. JSON output includes nextPageToken; pass it with --page-token to continue.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 1 || limit > 500 {
				return fmt.Errorf("--limit must be between 1 and 500")
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			res, err := api.NewCodeClient().ListRepositories(cmd.Context(), api.WithAuthenticationAndOrg(connect.NewRequest(&code.ListRepositoriesRequest{PageSize: limit, PageToken: page}), o.token, o.org))
			if err != nil {
				return err
			}
			repos := make([]coderepo.Repository, 0, len(res.Msg.Repositories))
			for _, r := range res.Msg.Repositories {
				repo, err := coderepo.Describe(o.org, r)
				if err != nil {
					return err
				}
				repos = append(repos, repo)
			}
			if o.output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Repositories  []coderepo.Repository `json:"repositories"`
					NextPageToken string                `json:"nextPageToken"`
				}{repos, res.Msg.NextPageToken})
			}
			for _, r := range repos {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", r.RepositoryID, r.Name, r.DefaultBranch); err != nil {
					return err
				}
			}
			if res.Msg.NextPageToken != "" {
				_, err = fmt.Fprintf(cmd.ErrOrStderr(), "More repositories available; use --output json and --page-token to continue.\n")
			}
			return err
		},
	}
	o.flags(cmd)
	cmd.Flags().Uint32Var(&limit, "limit", 50, "Repositories per page (1–500)")
	cmd.Flags().StringVar(&page, "page-token", "", "Continuation token from the previous JSON page")
	return cmd
}
