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

func NewCmdVars() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vars",
		Short: "Manage CI variables [beta]",
		Long:  "Manage variables for Depot CI workflows.\n\nThis command is in beta and subject to change.",
		Example: `  # Add a new variable
  depot ci vars add GITHUB_REPO
  depot ci vars add MY_SERVICE_NAME --value "my_service"

  # Add multiple variables at once
  depot ci vars add REGION=us-east-1 ENV=prod

  # Add a repo-specific variable
  depot ci vars add MY_SERVICE_NAME --repo owner/repo --value "my_service"

  # List all variables
  depot ci vars list

  # List variables including repo-specific overrides
  depot ci vars list --repo owner/repo

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
		Use:   "add [VAR_NAME | KEY=VALUE ...]",
		Short: "Add one or more CI variables",
		Long: `Add variables that can be used in Depot CI workflows.

Supports three modes:
  1. Single variable with --value flag: depot ci vars add VAR_NAME --value "val"
  2. Single variable with interactive prompt: depot ci vars add VAR_NAME
  3. Bulk KEY=VALUE pairs: depot ci vars add FOO=bar BAZ=qux

The --value flag cannot be used with KEY=VALUE pairs.
Use --repo to scope variables to a specific repository. Without --repo, variables
apply to all repositories in the organization.`,
		Example: `  # Add an org-wide variable with interactive prompt
  depot ci vars add GITHUB_REPO

  # Add an org-wide variable with value from command line
  depot ci vars add MY_SERVICE_NAME --value "my_service"

  # Add multiple variables at once
  depot ci vars add REGION=us-east-1 ENV=prod

  # Add a repo-specific variable
  depot ci vars add DEPLOY_ENV --repo owner/repo --value "production"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			// Allow migration of GH Vars to Depot CI via GH OIDC
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

				var variables []*civ2.VariableInput
				for _, arg := range args {
					parts := strings.SplitN(arg, "=", 2)
					if len(parts) != 2 || parts[0] == "" {
						return fmt.Errorf("invalid argument %q — expected KEY=VALUE format", arg)
					}
					variables = append(variables, &civ2.VariableInput{Name: parts[0], Value: parts[1]})
				}

				err := api.CIBatchAddVariables(ctx, tokenVal, orgID, variables, repo)
				if err != nil {
					return fmt.Errorf("failed to add variables: %w", err)
				}

				for _, v := range variables {
					fmt.Printf("Successfully added CI variable '%s' (%s)\n", v.Name, scope)
				}
				return nil
			}

			// Single mode: first arg is variable name
			if len(args) > 1 {
				return fmt.Errorf("too many arguments — did you mean to use KEY=VALUE format?")
			}

			varName := args[0]
			if varName == "" {
				return fmt.Errorf("variable name cannot be empty")
			}

			varValue := value
			if varValue == "" {
				varValue, err = helpers.PromptForValue(fmt.Sprintf("Enter value for variable '%s': ", varName))
				if err != nil {
					return fmt.Errorf("failed to read variable value: %w", err)
				}
			}

			err = api.CIAddVariable(ctx, tokenVal, orgID, varName, varValue, repo)
			if err != nil {
				return fmt.Errorf("failed to add CI variable: %w", err)
			}

			fmt.Printf("Successfully added CI variable '%s' (%s)\n", varName, scope)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&value, "value", "", "Variable value (will prompt if not provided)")
	cmd.Flags().StringVar(&repo, "repo", "", "Scope variable to a specific repository (e.g. owner/repo)")

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
		Long: `List all CI variables for your organization.
Use --repo to also show repo-specific variables that override org-wide values.`,
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

			variables, err := api.CIListVariables(ctx, tokenVal, orgID, repo)
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

			if repo != "" {
				fmt.Printf("%-30s %-20s %-40s %s\n", "NAME", "SCOPE", "DESCRIPTION", "CREATED")
				fmt.Printf("%-30s %-20s %-40s %s\n", strings.Repeat("-", 30), strings.Repeat("-", 20), strings.Repeat("-", 40), strings.Repeat("-", 20))
			} else {
				fmt.Printf("%-30s %-50s %s\n", "NAME", "DESCRIPTION", "CREATED")
				fmt.Printf("%-30s %-50s %s\n", strings.Repeat("-", 30), strings.Repeat("-", 50), strings.Repeat("-", 20))
			}

			for _, v := range variables {
				name := v.Name
				if len(name) > 30 {
					name = name[:27] + "..."
				}

				description := v.Description
				created := v.CreatedAt

				if repo != "" {
					if len(description) > 40 {
						description = description[:37] + "..."
					}
					scope := v.Scope
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

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&output, "output", "", "Output format (json)")
	cmd.Flags().StringVar(&repo, "repo", "", "Also show repo-specific variables for this repository (e.g. owner/repo)")

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
		Long: `Remove one or more CI variables from your organization.
Use --repo to remove repo-specific variables instead of org-wide ones.`,
		Example: `  # Remove an org-wide variable
  depot ci vars remove GITHUB_REPO

  # Remove a repo-specific variable
  depot ci vars remove GITHUB_REPO --repo owner/repo

  # Remove variables without confirmation prompt
  depot ci vars remove GITHUB_REPO MY_SERVICE_NAME --force`,
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
				prompt := fmt.Sprintf("Are you sure you want to remove %s CI variable(s) %s? (y/N): ", scope, names)
				y, err := helpers.PromptForYN(prompt)
				if err != nil {
					return fmt.Errorf("failed to read confirmation: %w", err)
				} else if !y {
					return nil
				}
			}

			for _, varName := range args {
				err := api.CIDeleteVariable(ctx, tokenVal, orgID, varName, repo)
				if err != nil {
					return fmt.Errorf("failed to remove CI variable '%s': %w", varName, err)
				}
				fmt.Printf("Successfully removed CI variable '%s' (%s)\n", varName, scope)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	cmd.Flags().StringVar(&repo, "repo", "", "Remove repo-specific variable instead of org-wide (e.g. owner/repo)")

	return cmd
}
