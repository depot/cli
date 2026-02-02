package sandbox

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
)

type templatesListOptions struct {
	output string
	token  string
	orgID  string
	stdout io.Writer
	stderr io.Writer
}

// NewCmdTemplatesList creates the sandbox templates list subcommand
func NewCmdTemplatesList() *cobra.Command {
	opts := &templatesListOptions{
		output: "table",
		stdout: os.Stdout,
		stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sandbox templates",
		Long: `List all sandbox templates for your organization.

Shows template name, SSH compatibility, source repository, and creation date.`,
		Example: `  # List all templates
  depot sandbox templates list

  # Output as JSON
  depot sandbox templates list --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTemplatesList(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.output, "output", "table", "Output format (table, json, csv)")
	cmd.Flags().StringVar(&opts.token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&opts.orgID, "org", "", "Organization ID")

	return cmd
}

type templateOutput struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	SSHCompatible    bool   `json:"ssh_compatible"`
	SourceRepository string `json:"source_repository,omitempty"`
	SourceBranch     string `json:"source_branch,omitempty"`
	CreatedAt        string `json:"created_at"`
}

func runTemplatesList(ctx context.Context, opts *templatesListOptions) error {
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

	req := &agentv1.ListSandboxTemplatesRequest{}

	res, err := sandboxClient.ListSandboxTemplates(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, opts.orgID))
	if err != nil {
		return fmt.Errorf("unable to list templates: %w", err)
	}

	templates := res.Msg.Templates

	outputs := make([]templateOutput, 0, len(templates))
	for _, t := range templates {
		out := templateOutput{
			ID:            t.Id,
			Name:          t.Name,
			SSHCompatible: t.SshCompatible,
			CreatedAt:     t.CreatedAt.AsTime().Format("2006-01-02 15:04:05"),
		}
		if t.Description != nil {
			out.Description = *t.Description
		}
		if t.SourceRepository != nil {
			out.SourceRepository = *t.SourceRepository
		}
		if t.SourceBranch != nil {
			out.SourceBranch = *t.SourceBranch
		}
		outputs = append(outputs, out)
	}

	switch opts.output {
	case "json":
		return outputTemplatesJSON(outputs, opts.stdout)
	case "csv":
		return outputTemplatesCSV(outputs, opts.stdout)
	default:
		return outputTemplatesTable(outputs, opts.stdout)
	}
}

func outputTemplatesJSON(templates []templateOutput, w io.Writer) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(templates)
}

func outputTemplatesCSV(templates []templateOutput, w io.Writer) error {
	writer := csv.NewWriter(w)
	defer writer.Flush()

	if err := writer.Write([]string{"id", "name", "ssh_compatible", "source_repository", "created_at"}); err != nil {
		return err
	}

	for _, t := range templates {
		sshCompat := "no"
		if t.SSHCompatible {
			sshCompat = "yes"
		}
		if err := writer.Write([]string{
			t.ID,
			t.Name,
			sshCompat,
			t.SourceRepository,
			t.CreatedAt,
		}); err != nil {
			return err
		}
	}

	return nil
}

func outputTemplatesTable(templates []templateOutput, w io.Writer) error {
	if len(templates) == 0 {
		fmt.Fprintln(w, "No templates found.")
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Create a template from a running sandbox:")
		fmt.Fprintln(w, "  depot sandbox templates create <sandbox-id> --name <name>")
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer tw.Flush()

	fmt.Fprintln(tw, "NAME\tID\tSSH\tSOURCE REPO\tCREATED")
	for _, t := range templates {
		sshStatus := "no"
		if t.SSHCompatible {
			sshStatus = "yes"
		}
		repo := t.SourceRepository
		if len(repo) > 40 {
			repo = repo[:37] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			t.Name,
			t.ID,
			sshStatus,
			repo,
			t.CreatedAt,
		)
	}

	return nil
}
