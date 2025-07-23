package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

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

func NewCmdClaudeSecretsList() *cobra.Command {
	var (
		orgID  string
		token  string
		output string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all secrets",
		Long:  `List all secrets available for Claude remote sessions in your organization.`,
		Example: `  # List all secrets
  depot claude secrets list
  
  # List secrets for a specific organization
  depot claude secrets list --org my-org-id
  
  # List secrets in JSON format
  depot claude secrets list --output json`,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// If org ID is not set, use the current organization from config
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

			// Create the request
			client := api.NewClaudeClient()
			req := &agentv1.ListSecretsRequest{}
			if orgID != "" {
				req.OrganizationId = &orgID
			}

			// List secrets
			resp, err := client.ListSecrets(ctx, api.WithAuthentication(connect.NewRequest(req), tokenVal))
			if err != nil {
				return fmt.Errorf("failed to list secrets: %w", err)
			}

			// Format output
			if output == "json" {
				// JSON output
				type secretJSON struct {
					Name        string `json:"name"`
					Description string `json:"description,omitempty"`
					CreatedAt   string `json:"created_at,omitempty"`
					UpdatedAt   string `json:"updated_at,omitempty"`
				}
				var secrets []secretJSON
				for _, secret := range resp.Msg.Secrets {
					s := secretJSON{
						Name: secret.SecretName,
					}
					if secret.Description != nil {
						s.Description = *secret.Description
					}
					if secret.CreatedAt != nil {
						s.CreatedAt = secret.CreatedAt.AsTime().Format(time.RFC3339)
					}
					if secret.UpdatedAt != nil {
						s.UpdatedAt = secret.UpdatedAt.AsTime().Format(time.RFC3339)
					}
					secrets = append(secrets, s)
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(secrets)
			}

			// Table output
			if len(resp.Msg.Secrets) == 0 {
				fmt.Println("No secrets found.")
				return nil
			}

			// Print header
			fmt.Printf("%-30s %-50s %s\n", "NAME", "DESCRIPTION", "CREATED")
			fmt.Printf("%-30s %-50s %s\n", strings.Repeat("-", 30), strings.Repeat("-", 50), strings.Repeat("-", 20))

			// Print secrets
			for _, secret := range resp.Msg.Secrets {
				name := secret.SecretName
				if len(name) > 30 {
					name = name[:27] + "..."
				}

				description := ""
				if secret.Description != nil {
					description = *secret.Description
					if len(description) > 50 {
						description = description[:47] + "..."
					}
				}

				created := ""
				if secret.CreatedAt != nil {
					created = secret.CreatedAt.AsTime().Format("2006-01-02 15:04:05")
				}

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
	password := stripANSI(string(bytePassword))
	fmt.Println()

	return string(password), nil
}

func stripANSI(s string) string {
	// Matches ESC followed by bracket and any sequence of characters ending in a letter
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansiRegex.ReplaceAllString(s, "")
}
