package claude

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func NewCmdClaudeSecrets() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage secrets for Claude remote sessions",
		Long: `Manage secrets that can be used in Claude remote sessions.
Secrets are stored securely in AWS Secrets Manager and scoped to your organization.`,
		Example: `  # Add a new secret
  depot claude secrets add GITHUB_TOKEN
  depot claude secrets add MY_API_KEY --value "secret-value"
  
  # List all secrets
  depot claude secrets list
  
  # Remove a secret
  depot claude secrets remove GITHUB_TOKEN`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(NewCmdClaudeSecretsAdd())
	cmd.AddCommand(NewCmdClaudeSecretsList())
	cmd.AddCommand(NewCmdClaudeSecretsRemove())

	return cmd
}

func NewCmdClaudeSecretsAdd() *cobra.Command {
	var (
		orgID       string
		token       string
		value       string
		description string
	)

	cmd := &cobra.Command{
		Use:   "add SECRET_NAME",
		Short: "Add a new secret",
		Long: `Add a new secret that can be used in Claude remote sessions.
If --value is not provided, you will be prompted to enter the secret value securely.`,
		Example: `  # Add a secret with interactive prompt
  depot claude secrets add GITHUB_TOKEN
  
  # Add a secret with value from command line
  depot claude secrets add MY_API_KEY --value "secret-value"
  
  # Add a secret with description
  depot claude secrets add DATABASE_URL --description "Production database connection string"`,
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

			tokenVal, err := helpers.ResolveToken(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			secretValue := value
			if secretValue == "" {
				secretValue, err = promptForSecret(fmt.Sprintf("Enter value for secret '%s': ", secretName))
				if err != nil {
					return fmt.Errorf("failed to read secret value: %w", err)
				}
			}

			client := api.NewClaudeClient()
			req := &agentv1.AddSecretRequest{
				SecretName:  secretName,
				SecretValue: secretValue,
			}
			if orgID != "" {
				req.OrganizationId = &orgID
			}
			if description != "" {
				req.SecretDescription = &description
			}

			_, err = client.AddSecret(ctx, api.WithAuthentication(connect.NewRequest(req), tokenVal))
			if err != nil {
				return fmt.Errorf("failed to add secret: %w", err)
			}

			fmt.Printf("Successfully added secret '%s'\n", secretName)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&value, "value", "", "Secret value (will prompt if not provided)")
	cmd.Flags().StringVar(&description, "description", "", "Description of the secret")

	return cmd
}

func NewCmdClaudeSecretsRemove() *cobra.Command {
	var (
		orgID string
		token string
		force bool
	)

	cmd := &cobra.Command{
		Use:   "remove SECRET_NAME",
		Short: "Remove a secret",
		Long:  `Remove a secret from your organization.`,
		Example: `  # Remove a secret
  depot claude secrets remove GITHUB_TOKEN
  
  # Remove a secret without confirmation prompt
  depot claude secrets remove MY_API_KEY --force`,
		Aliases: []string{"rm", "delete", "del"},
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

			tokenVal, err := helpers.ResolveToken(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			if !force {
				reader := bufio.NewReader(os.Stdin)
				fmt.Printf("Are you sure you want to remove secret '%s'? (y/N): ", secretName)
				response, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("failed to read confirmation: %w", err)
				}
				response = strings.TrimSpace(strings.ToLower(response))
				if response != "y" && response != "yes" {
					fmt.Println("Secret removal cancelled")
					return nil
				}
			}

			client := api.NewClaudeClient()
			req := &agentv1.RemoveSecretRequest{
				SecretName: secretName,
			}
			if orgID != "" {
				req.OrganizationId = &orgID
			}

			_, err = client.RemoveSecret(ctx, api.WithAuthentication(connect.NewRequest(req), tokenVal))
			if err != nil {
				return fmt.Errorf("failed to remove secret: %w", err)
			}

			fmt.Printf("Successfully removed secret '%s'\n", secretName)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")

	return cmd
}

func promptForSecret(prompt string) (string, error) {
	fmt.Print(prompt)

	bytePassword, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return "", err
	}
	fmt.Println()

	return string(bytePassword), nil
}
