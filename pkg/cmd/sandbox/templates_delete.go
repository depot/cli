package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
)

type templatesDeleteOptions struct {
	token  string
	orgID  string
	stdout io.Writer
	stderr io.Writer
}

// NewCmdTemplatesDelete creates the sandbox templates delete subcommand
func NewCmdTemplatesDelete() *cobra.Command {
	opts := &templatesDeleteOptions{
		stdout: os.Stdout,
		stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "delete <template-id>",
		Short: "Delete a sandbox template",
		Long: `Delete a sandbox template.

This removes the template from your organization. Existing sandboxes created
from this template are not affected.`,
		Example: `  # Delete a template by ID
  depot sandbox templates delete tpl_abc123`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTemplatesDelete(cmd.Context(), args[0], opts)
		},
	}

	cmd.Flags().StringVar(&opts.token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&opts.orgID, "org", "", "Organization ID")

	return cmd
}

func runTemplatesDelete(ctx context.Context, templateID string, opts *templatesDeleteOptions) error {
	token, err := helpers.ResolveOrgAuth(ctx, opts.token)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}

	if opts.orgID == "" {
		opts.orgID = os.Getenv("DEPOT_ORG_ID")
	}
	if opts.orgID == "" {
		opts.orgID = config.GetCurrentOrganization()
	}

	sandboxClient := api.NewSandboxClient()

	req := &agentv1.DeleteSandboxTemplateRequest{
		TemplateId: templateID,
	}

	_, err = sandboxClient.DeleteSandboxTemplate(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, opts.orgID))
	if err != nil {
		return fmt.Errorf("unable to delete template: %w", err)
	}

	fmt.Fprintf(opts.stdout, "Template %s deleted successfully.\n", templateID)
	return nil
}
