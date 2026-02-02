package sandbox

import (
	"github.com/spf13/cobra"
)

// NewCmdTemplates creates the sandbox templates parent command
func NewCmdTemplates() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "templates",
		Short: "Manage sandbox templates",
		Long: `Create, list, and manage sandbox templates.

Templates capture the filesystem state of a configured sandbox,
allowing you to quickly spin up new sandboxes with pre-installed
dependencies and tools.`,
		Example: `  # List all templates
  depot sandbox templates list

  # Create a template from a running sandbox
  depot sandbox templates create <sandbox-id> --name my-template

  # Delete a template
  depot sandbox templates delete <template-id>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(NewCmdTemplatesList())
	cmd.AddCommand(NewCmdTemplatesCreate())
	cmd.AddCommand(NewCmdTemplatesDelete())

	return cmd
}
