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

func NewCmdVars() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vars",
		Short: "Manage CI variables [beta]",
		Long:  "Manage variables for Depot CI workflows.\n\nThis command is in beta and subject to change.",
		Example: `  # Add a new variable
  depot ci vars add GITHUB_REPO
  depot ci vars add MY_SERVICE_NAME --value "my_service"

  # List all variables
  depot ci vars list

  # Remove a variable
  depot ci vars remove GITHUB_REPO`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(NewCmdVarsAdd())
	cmd.AddCommand(NewCmdVarsList())
	cmd.AddCommand(NewCmdVarsRemove())
	return cmd
}

func NewCmdVarsAdd() *cobra.Command {
	var (
		orgID string
		token string
		value string
		repo  string
	)

	cmd := &cobra.Command{
		Use:   "add VAR_NAME",
		Short: "Add a new CI variable",
		Long: `Add a new variable that can be used in Depot CI workflows.
If --value is not provided, you will be prompted to enter the secret value securely.`,
		Example: `  # Add a variable with interactive prompt
  depot ci vars add GITHUB_REPO

  # Add a variable with value from command line
  depot ci vars add MY_SERVICE_NAME --value "my_service"

  # Add a variable with description
  depot ci vars add MY_SERVICE_NAME --description "Name of the service for matrix tests"

  # Add a repo-scoped variable
  depot ci vars add REPO_VAR --repo owner/repo`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			varName := args[0]

			if varName == "" {
				return fmt.Errorf("variable name cannot be empty")
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

			varValue := value
			if varValue == "" {
				varValue, err = helpers.PromptForValue(fmt.Sprintf("Enter value for variable '%s': ", varName))
				if err != nil {
					return fmt.Errorf("failed to read variable value: %w", err)
				}
			}

			repoTrimmed := strings.TrimSpace(repo)
			err = api.CIAddVariable(ctx, tokenVal, orgID, varName, varValue, repoTrimmed)
			if err != nil {
				return fmt.Errorf("failed to add CI variable: %w", err)
			}

			scopeDesc := "(all repos)"
			if repoTrimmed != "" {
				scopeDesc = repoTrimmed
			}
			fmt.Printf("Successfully added CI variable '%s' to %s\n", varName, scopeDesc)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&value, "value", "", "Variable value (will prompt if not provided)")
	cmd.Flags().StringVar(&repo, "repo", "", "Scope to a specific repo (owner/repo); omit for all repos")

	return cmd
}

func NewCmdVarsList() *cobra.Command {
	var (
		orgID  string
		token  string
		output string
		repo   string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all CI variables",
		Long:  "List all CI variables for your organization.",
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

			variables, err := api.CIListVariables(ctx, tokenVal, orgID, strings.TrimSpace(repo))
			if err != nil {
				return fmt.Errorf("failed to list CI variables: %w", err)
			}

			if output == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(variables)
			}

			if len(variables) == 0 {
				fmt.Println("No CI variables found.")
				return nil
			}

			fmt.Printf("%-30s %-50s %-25s %s\n", "NAME", "DESCRIPTION", "REPO", "CREATED")
			fmt.Printf("%-30s %-50s %-25s %s\n", strings.Repeat("-", 30), strings.Repeat("-", 50), strings.Repeat("-", 25), strings.Repeat("-", 20))

			for _, v := range variables {
				name := v.Name
				if len(name) > 30 {
					name = name[:27] + "..."
				}

				description := v.Description
				if len(description) > 50 {
					description = description[:47] + "..."
				}

				repoScope := v.Repo
				if repoScope == "" {
					repoScope = "(all repos)"
				}
				if len(repoScope) > 25 {
					repoScope = repoScope[:22] + "..."
				}

				created := v.CreatedAt

				fmt.Printf("%-30s %-50s %-25s %s\n", name, description, repoScope, created)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&output, "output", "", "Output format (json)")
	cmd.Flags().StringVar(&repo, "repo", "", "Filter to variables that apply to this repo (owner/repo); omit for all")

	return cmd
}

func NewCmdVarsRemove() *cobra.Command {
	var (
		orgID string
		token string
		force bool
		repo  string
	)

	cmd := &cobra.Command{
		Use:   "remove VAR_NAME [VAR_NAME...]",
		Short: "Remove one or more CI variables",
		Long:  "Remove one or more CI variables from your organization.",
		Example: `  # Remove a variable
  depot ci vars remove GITHUB_REPO

  # Remove multiple variables
  depot ci vars remove GITHUB_REPO MY_SERVICE_NAME DEPLOY_ENV

  # Remove variables without confirmation prompt
  depot ci vars remove GITHUB_REPO MY_SERVICE_NAME --force

  # Remove a repo-scoped variable
  depot ci vars remove REPO_VAR --repo owner/repo`,
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

			if !force {
				names := strings.Join(args, ", ")
				scopeDesc := "(all repos)"
				if strings.TrimSpace(repo) != "" {
					scopeDesc = strings.TrimSpace(repo)
				}
				prompt := fmt.Sprintf("Are you sure you want to remove CI variable(s) %s from %s? (y/N): ", names, scopeDesc)
				y, err := helpers.PromptForYN(prompt)
				if err != nil {
					return fmt.Errorf("failed to read confirmation: %w", err)
				} else if !y {
					return nil
				}
			}

			scopeDesc := "(all repos)"
			if strings.TrimSpace(repo) != "" {
				scopeDesc = strings.TrimSpace(repo)
			}
			for _, varName := range args {
				err := api.CIDeleteVariable(ctx, tokenVal, orgID, varName, strings.TrimSpace(repo))
				if err != nil {
					return fmt.Errorf("failed to remove CI variable '%s': %w", varName, err)
				}
				fmt.Printf("Successfully removed CI variable '%s' from %s\n", varName, scopeDesc)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	cmd.Flags().StringVar(&repo, "repo", "", "Remove the repo-scoped entry (owner/repo); omit for all-repos entry")

	return cmd
}
