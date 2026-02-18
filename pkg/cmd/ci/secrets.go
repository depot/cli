package ci

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdSecrets() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage CI secrets [beta]",
		Long:  "Manage secrets for Depot CI workflows.\n\nThis command is in beta and subject to change.",
		Example: `  # Add a new secret
  depot ci secrets add GITHUB_TOKEN
  depot ci secrets add MY_API_KEY --value "secret-value"

  # List all secrets
  depot ci secrets list

  # Remove a secret
  depot ci secrets remove GITHUB_TOKEN`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(NewCmdSecretsAdd())
	cmd.AddCommand(NewCmdSecretsList())
	cmd.AddCommand(NewCmdSecretsRemove())

	return cmd
}

func NewCmdSecretsAdd() *cobra.Command {
	var (
		orgID       string
		token       string
		value       string
		description string
	)

	cmd := &cobra.Command{
		Use:   "add SECRET_NAME",
		Short: "Add a new CI secret",
		Long: `Add a new secret that can be used in Depot CI workflows.
If --value is not provided, you will be prompted to enter the secret value securely.`,
		Example: `  # Add a secret with interactive prompt
  depot ci secrets add GITHUB_TOKEN

  # Add a secret with value from command line
  depot ci secrets add MY_API_KEY --value "secret-value"

  # Add a secret with description
  depot ci secrets add DATABASE_URL --description "Production database connection string"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			secretName := args[0]

			if secretName == "" {
				return fmt.Errorf("secret name cannot be empty")
			}

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			secretValue := value
			if secretValue == "" {
				secretValue, err = helpers.PromptForSecret(fmt.Sprintf("Enter value for secret '%s': ", secretName))
				if err != nil {
					return fmt.Errorf("failed to read secret value: %w", err)
				}
			}

			err = api.CIAddSecretWithDescription(ctx, tokenVal, orgID, secretName, secretValue, description)
			if err != nil {
				return fmt.Errorf("failed to add secret: %w", err)
			}

			fmt.Printf("Successfully added CI secret '%s'\n", secretName)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&value, "value", "", "Secret value (will prompt if not provided)")
	cmd.Flags().StringVar(&description, "description", "", "Description of the secret")

	return cmd
}

func NewCmdSecretsList() *cobra.Command {
	var (
		orgID  string
		token  string
		output string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all CI secrets",
		Long:  `List all secrets available for Depot CI workflows in your organization.`,
		Example: `  # List all secrets
  depot ci secrets list

  # List secrets for a specific organization
  depot ci secrets list --org my-org-id

  # List secrets in JSON format
  depot ci secrets list --output json`,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			secrets, err := api.CIListSecrets(ctx, tokenVal, orgID)
			if err != nil {
				return fmt.Errorf("failed to list secrets: %w", err)
			}

			if output == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(secrets)
			}

			if len(secrets) == 0 {
				fmt.Println("No secrets found.")
				return nil
			}

			fmt.Printf("%-30s %-50s %s\n", "NAME", "DESCRIPTION", "CREATED")
			fmt.Printf("%-30s %-50s %s\n", strings.Repeat("-", 30), strings.Repeat("-", 50), strings.Repeat("-", 20))

			for _, secret := range secrets {
				name := secret.Name
				if len(name) > 30 {
					name = name[:27] + "..."
				}

				description := secret.Description
				if len(description) > 50 {
					description = description[:47] + "..."
				}

				created := secret.CreatedAt

				fmt.Printf("%-30s %-50s %s\n", name, description, created)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&output, "output", "", "Output format (json)")

	return cmd
}

func NewCmdSecretsRemove() *cobra.Command {
	var (
		orgID string
		token string
		force bool
	)

	cmd := &cobra.Command{
		Use:   "remove SECRET_NAME",
		Short: "Remove a CI secret",
		Long:  `Remove a CI secret from your organization.`,
		Example: `  # Remove a secret
  depot ci secrets remove GITHUB_TOKEN

  # Remove a secret without confirmation prompt
  depot ci secrets remove MY_API_KEY --force`,
		Aliases: []string{"rm"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			secretName := args[0]

			if secretName == "" {
				return fmt.Errorf("secret name cannot be empty")
			}

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			if !force {
				prompt := fmt.Sprintf("Are you sure you want to remove CI secret '%s'? (y/N): ", secretName)
				y, err := helpers.PromptForYN(prompt)
				if err != nil {
					return fmt.Errorf("failed to read confirmation: %w", err)
				} else if !y {
					return nil
				}
			}

			err = api.CIDeleteSecret(ctx, tokenVal, orgID, secretName)
			if err != nil {
				return fmt.Errorf("failed to remove secret: %w", err)
			}

			fmt.Printf("Successfully removed CI secret '%s'\n", secretName)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")

	return cmd
}
