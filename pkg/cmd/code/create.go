package code

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	gitv1 "github.com/depot/cli/pkg/proto/depot/git/v1"
	"github.com/spf13/cobra"
)

func newCreate() *cobra.Command {
	var org, token, branch, output string
	cmd := &cobra.Command{
		Use: "create <name>", Short: "Create a standalone Depot Code repository",
		Long: `Create an empty Git repository hosted by Depot. Names can include slash-separated
namespaces, such as team/service. Uses the current organization unless --org is set.
Requires an organization owner credential. Prints the repository ID and HTTPS clone URL.

Creation does not initialize a local checkout, configure Git credentials, or push commits.
If a request times out, check whether the repository was created before retrying.`,
		Example: `  # Create a repository in the current organization
  depot code repo create my-service

  # Choose an organization and default branch, with scriptable output
  depot code repo create team/service --org <org-id> --default-branch develop --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("repository name must not be blank")
			}
			if strings.TrimSpace(branch) == "" {
				return fmt.Errorf("--default-branch must not be blank")
			}
			if output != "" && output != "json" {
				return fmt.Errorf("unsupported output format %q: use json", output)
			}
			if !cmd.Flags().Changed("org") {
				org = config.GetCurrentOrganization()
			}
			if strings.TrimSpace(org) == "" {
				return fmt.Errorf("organization is required: use --org or `depot org switch`")
			}
			resolvedToken, err := helpers.ResolveOrgAuth(cmd.Context(), token)
			if err != nil {
				return err
			}
			if resolvedToken == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}
			response, err := api.NewGitRepositoryClient().Create(cmd.Context(), api.WithAuthenticationAndOrg(connect.NewRequest(&gitv1.CreateRequest{
				CanonicalName: args[0], DefaultBranch: branch, ObjectFormat: gitv1.ObjectFormat_OBJECT_FORMAT_SHA1,
			}), resolvedToken, org))
			if err != nil {
				return err
			}
			if response.Msg.GetId() == "" {
				return fmt.Errorf("create repository returned no repository ID; check whether it was created before retrying")
			}
			cloneURL := (&url.URL{Scheme: "https", Host: org + ".code.depot.dev", Path: "/" + args[0]}).String()
			if output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					RepositoryID  string `json:"repositoryId"`
					Name          string `json:"name"`
					DefaultBranch string `json:"defaultBranch"`
					CloneURL      string `json:"cloneUrl"`
				}{response.Msg.Id, args[0], branch, cloneURL})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created %s (%s)\nClone URL: %s\n", args[0], response.Msg.Id, cloneURL)
			return err
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Organization ID (defaults to the current organization)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&branch, "default-branch", "main", "Default branch name")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	return cmd
}
