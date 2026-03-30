package ci

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
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

  # Add multiple secrets at once
  depot ci secrets add FOO=bar BAZ=qux

  # Add a repo-specific secret
  depot ci secrets add MY_API_KEY --repo owner/repo --value "secret-value"

  # List all secrets
  depot ci secrets list

  # List secrets including repo-specific overrides
  depot ci secrets list --repo owner/repo

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
		repo        string
	)

	cmd := &cobra.Command{
		Use:   "add [SECRET_NAME | KEY=VALUE ...]",
		Short: "Add one or more CI secrets",
		Long: `Add secrets that can be used in Depot CI workflows.

Supports three modes:
  1. Single secret with --value flag: depot ci secrets add SECRET_NAME --value "val"
  2. Single secret with interactive prompt: depot ci secrets add SECRET_NAME
  3. Bulk KEY=VALUE pairs: depot ci secrets add FOO=bar BAZ=qux

The --value and --description flags cannot be used with KEY=VALUE pairs.
Use --repo to scope secrets to a specific repository. Without --repo, secrets
apply to all repositories in the organization.`,
		Example: `  # Add an org-wide secret with interactive prompt
  depot ci secrets add GITHUB_TOKEN

  # Add an org-wide secret with value from command line
  depot ci secrets add MY_API_KEY --value "secret-value"

  # Add multiple secrets at once
  depot ci secrets add FOO=bar BAZ=qux

  # Add a repo-specific secret
  depot ci secrets add DATABASE_URL --repo owner/repo --value "prod-db-url"

  # Add a secret with description
  depot ci secrets add DATABASE_URL --description "Production database connection string"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			// Allow migration of GH Secrets to Depot CI via GH OIDC
			tokenVal, err := helpers.ResolveProjectAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			scope := "org-wide"
			if repo != "" {
				scope = repo
			}

			// Detect KEY=VALUE pairs
			hasKVPairs := false
			for _, arg := range args {
				if strings.Contains(arg, "=") {
					hasKVPairs = true
					break
				}
			}

			if hasKVPairs {
				// Bulk mode: all args must be KEY=VALUE
				if value != "" {
					return fmt.Errorf("cannot use --value with KEY=VALUE arguments")
				}
				if description != "" {
					return fmt.Errorf("cannot use --description with KEY=VALUE arguments")
				}

				var secrets []*civ2.SecretInput
				for _, arg := range args {
					parts := strings.SplitN(arg, "=", 2)
					if len(parts) != 2 || parts[0] == "" {
						return fmt.Errorf("invalid argument %q — expected KEY=VALUE format", arg)
					}
					secrets = append(secrets, &civ2.SecretInput{Name: parts[0], Value: parts[1]})
				}

				err := api.CIBatchAddSecrets(ctx, tokenVal, orgID, secrets, repo)
				if err != nil {
					return fmt.Errorf("failed to add secrets: %w", err)
				}

				for _, s := range secrets {
					fmt.Printf("Successfully added CI secret '%s' (%s)\n", s.Name, scope)
				}
				return nil
			}

			// Single mode: first arg is secret name
			if len(args) > 1 {
				return fmt.Errorf("too many arguments — did you mean to use KEY=VALUE format?")
			}

			secretName := args[0]
			if secretName == "" {
				return fmt.Errorf("secret name cannot be empty")
			}

			secretValue := value
			if secretValue == "" {
				secretValue, err = helpers.PromptForSecret(fmt.Sprintf("Enter value for secret '%s': ", secretName))
				if err != nil {
					return fmt.Errorf("failed to read secret value: %w", err)
				}
			}

			err = api.CIAddSecretWithDescription(ctx, tokenVal, orgID, secretName, secretValue, description, repo)
			if err != nil {
				return fmt.Errorf("failed to add secret: %w", err)
			}

			fmt.Printf("Successfully added CI secret '%s' (%s)\n", secretName, scope)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&value, "value", "", "Secret value (will prompt if not provided)")
	cmd.Flags().StringVar(&description, "description", "", "Description of the secret")
	cmd.Flags().StringVar(&repo, "repo", "", "Scope secret to a specific repository (e.g. owner/repo)")

	return cmd
}

func NewCmdSecretsList() *cobra.Command {
	var (
		orgID  string
		token  string
		output string
		repo   string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all CI secrets",
		Long: `List all secrets available for Depot CI workflows in your organization.
Use --repo to also show repo-specific secrets that override org-wide values.`,
		Example: `  # List org-wide secrets
  depot ci secrets list

  # List org-wide and repo-specific secrets
  depot ci secrets list --repo owner/repo

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

			secrets, err := api.CIListSecrets(ctx, tokenVal, orgID, repo)
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

			if repo != "" {
				fmt.Printf("%-30s %-20s %-40s %s\n", "NAME", "SCOPE", "DESCRIPTION", "CREATED")
				fmt.Printf("%-30s %-20s %-40s %s\n", strings.Repeat("-", 30), strings.Repeat("-", 20), strings.Repeat("-", 40), strings.Repeat("-", 20))
			} else {
				fmt.Printf("%-30s %-50s %s\n", "NAME", "DESCRIPTION", "CREATED")
				fmt.Printf("%-30s %-50s %s\n", strings.Repeat("-", 30), strings.Repeat("-", 50), strings.Repeat("-", 20))
			}

			for _, secret := range secrets {
				name := secret.Name
				if len(name) > 30 {
					name = name[:27] + "..."
				}

				description := secret.Description
				created := secret.CreatedAt

				if repo != "" {
					if len(description) > 40 {
						description = description[:37] + "..."
					}
					scope := secret.Scope
					if len(scope) > 20 {
						scope = scope[:17] + "..."
					}
					fmt.Printf("%-30s %-20s %-40s %s\n", name, scope, description, created)
				} else {
					if len(description) > 50 {
						description = description[:47] + "..."
					}
					fmt.Printf("%-30s %-50s %s\n", name, description, created)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&output, "output", "", "Output format (json)")
	cmd.Flags().StringVar(&repo, "repo", "", "Also show repo-specific secrets for this repository (e.g. owner/repo)")

	return cmd
}

func NewCmdSecretsRemove() *cobra.Command {
	var (
		orgID string
		token string
		force bool
		repo  string
	)

	cmd := &cobra.Command{
		Use:   "remove SECRET_NAME [SECRET_NAME...]",
		Short: "Remove one or more CI secrets",
		Long: `Remove one or more CI secrets from your organization.
Use --repo to remove repo-specific secrets instead of org-wide ones.`,
		Example: `  # Remove an org-wide secret
  depot ci secrets remove GITHUB_TOKEN

  # Remove a repo-specific secret
  depot ci secrets remove GITHUB_TOKEN --repo owner/repo

  # Remove multiple secrets without confirmation prompt
  depot ci secrets remove GITHUB_TOKEN MY_API_KEY --force`,
		Aliases: []string{"rm"},
		Args:    cobra.MinimumNArgs(1),
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

			scope := "org-wide"
			if repo != "" {
				scope = repo
			}

			if !force {
				names := strings.Join(args, ", ")
				prompt := fmt.Sprintf("Are you sure you want to remove %s CI secret(s) %s? (y/N): ", scope, names)
				y, err := helpers.PromptForYN(prompt)
				if err != nil {
					return fmt.Errorf("failed to read confirmation: %w", err)
				} else if !y {
					return nil
				}
			}

			for _, secretName := range args {
				err := api.CIDeleteSecret(ctx, tokenVal, orgID, secretName, repo)
				if err != nil {
					return fmt.Errorf("failed to remove secret '%s': %w", secretName, err)
				}
				fmt.Printf("Successfully removed CI secret '%s' (%s)\n", secretName, scope)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	cmd.Flags().StringVar(&repo, "repo", "", "Remove repo-specific secret instead of org-wide (e.g. owner/repo)")

	return cmd
}
