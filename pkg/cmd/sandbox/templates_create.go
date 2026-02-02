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

type templatesCreateOptions struct {
	name        string
	description string
	token       string
	orgID       string
	stdout      io.Writer
	stderr      io.Writer
}

// NewCmdTemplatesCreate creates the sandbox templates create subcommand
func NewCmdTemplatesCreate() *cobra.Command {
	opts := &templatesCreateOptions{
		stdout: os.Stdout,
		stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "create <sandbox-id>",
		Short: "Create a template from a running sandbox",
		Long: `Create a new sandbox template by capturing the filesystem state of a running sandbox.

Templates capture installed dependencies, configuration, and workspace files,
allowing you to quickly spin up new sandboxes with the same environment.

The sandbox must be running (not completed) to create a template from it.`,
		Example: `  # Create a template from a running sandbox
  depot sandbox templates create sb_abc123 --name my-dev-env

  # Create with description
  depot sandbox templates create sb_abc123 --name nodejs-18 --description "Node.js 18 with common tools"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTemplatesCreate(cmd.Context(), args[0], opts)
		},
	}

	cmd.Flags().StringVar(&opts.name, "name", "", "Name for the template (required)")
	cmd.Flags().StringVar(&opts.description, "description", "", "Description for the template")
	cmd.Flags().StringVar(&opts.token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&opts.orgID, "org", "", "Organization ID")

	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func runTemplatesCreate(ctx context.Context, sandboxID string, opts *templatesCreateOptions) error {
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

	req := &agentv1.CreateSandboxTemplateRequest{
		SandboxId: sandboxID,
		Name:      opts.name,
	}
	if opts.description != "" {
		req.Description = &opts.description
	}

	fmt.Fprintf(opts.stdout, "Creating template from sandbox %s...\n", sandboxID)

	res, err := sandboxClient.CreateSandboxTemplate(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, opts.orgID))
	if err != nil {
		return fmt.Errorf("unable to create template: %w", err)
	}

	template := res.Msg.Template
	fmt.Fprintf(opts.stdout, "\nTemplate created successfully!\n")
	fmt.Fprintf(opts.stdout, "  ID:   %s\n", template.Id)
	fmt.Fprintf(opts.stdout, "  Name: %s\n", template.Name)
	if template.Description != nil && *template.Description != "" {
		fmt.Fprintf(opts.stdout, "  Description: %s\n", *template.Description)
	}
	fmt.Fprintf(opts.stdout, "  SSH Compatible: %v\n", template.SshCompatible)

	fmt.Fprintf(opts.stdout, "\nUse this template to start a new sandbox:\n")
	fmt.Fprintf(opts.stdout, "  depot sandbox start --ssh --template %s\n", template.Name)

	return nil
}
