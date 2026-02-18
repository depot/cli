package ci

import (
	"bufio"
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
		Short: "Manage CI variables",
		Long:  "Manage variables for Depot CI workflows.",
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
	)

	cmd := &cobra.Command{
		Use:   "add VAR_NAME",
		Short: "Add a new CI variable",
		Long:  "Add a new CI variable to your organization.",
		Args:  cobra.ExactArgs(1),
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
				fmt.Print("Enter value: ")
				reader := bufio.NewReader(os.Stdin)
				input, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("failed to read variable value: %w", err)
				}
				varValue = strings.TrimSpace(input)
			}

			err = api.CIAddVariable(ctx, tokenVal, orgID, varName, varValue)
			if err != nil {
				return fmt.Errorf("failed to add CI variable: %w", err)
			}

			fmt.Printf("Successfully added CI variable '%s'\n", varName)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&value, "value", "", "Variable value (will prompt if not provided)")

	return cmd
}

func NewCmdVarsList() *cobra.Command {
	var (
		orgID  string
		token  string
		output string
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

			variables, err := api.CIListVariables(ctx, tokenVal, orgID)
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

			fmt.Printf("%-30s %-50s %s\n", "NAME", "DESCRIPTION", "CREATED")
			fmt.Printf("%-30s %-50s %s\n", strings.Repeat("-", 30), strings.Repeat("-", 50), strings.Repeat("-", 20))

			for _, v := range variables {
				name := v.Name
				if len(name) > 30 {
					name = name[:27] + "..."
				}

				description := v.Description
				if len(description) > 50 {
					description = description[:47] + "..."
				}

				created := v.CreatedAt

				fmt.Printf("%-30s %-50s %s\n", name, description, created)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&output, "output", "", "Output format (json)")

	return cmd
}

func NewCmdVarsRemove() *cobra.Command {
	var (
		orgID string
		token string
		force bool
	)

	cmd := &cobra.Command{
		Use:   "remove VAR_NAME",
		Short: "Remove a CI variable",
		Long:  "Remove a CI variable from your organization.",
		Args:  cobra.ExactArgs(1),
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

			if !force {
				reader := bufio.NewReader(os.Stdin)
				fmt.Printf("Are you sure you want to remove CI variable '%s'? (y/N): ", varName)
				response, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("failed to read confirmation: %w", err)
				}
				response = strings.TrimSpace(strings.ToLower(response))
				if response != "y" && response != "yes" {
					fmt.Println("CI variable removal cancelled")
					return nil
				}
			}

			err = api.CIDeleteVariable(ctx, tokenVal, orgID, varName)
			if err != nil {
				return fmt.Errorf("failed to remove CI variable: %w", err)
			}

			fmt.Printf("Successfully removed CI variable '%s'\n", varName)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")

	return cmd
}
