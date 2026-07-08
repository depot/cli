package ci

import (
	"fmt"
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

  # Set a named variable variant
  depot ci vars set DEPLOY_ENV production --repo owner/repo --value "production"

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
	cmd.AddCommand(NewCmdVarsSet())
	cmd.AddCommand(NewCmdVarsAdd())
	cmd.AddCommand(NewCmdVarsList())
	cmd.AddCommand(NewCmdVarsRemove())
	return cmd
}

func NewCmdVarsSet() *cobra.Command {
	var (
		orgID       string
		token       string
		value       string
		description string
		repo        []string
		environment []string
		branch      []string
		workflow    []string
	)

	cmd := &cobra.Command{
		Use:   "set <variable-name> [variant]",
		Short: "Create or update a CI variable variant",
		Long: `Create or update a CI variable variant.

Variants let one variable name have different values for matching repositories,
environments, branches, or workflows. When variant is omitted, the variant is
named "default".`,
		Example: `  # Set the default variant
  depot ci vars set DEPLOY_ENV --value "staging"

  # Set a named variant
  depot ci vars set DEPLOY_ENV production --value "production"

  # Set a variant that only applies to matching workflow runs
  depot ci vars set DEPLOY_ENV production --repo owner/repo --env production --branch main --workflow deploy.yml --value "production"

  # Set a variant that applies to multiple branches
  depot ci vars set DEPLOY_ENV release --repo owner/repo --branch main --branch 'release/*' --value "release"`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			if value == "" && !helpers.IsStdinTerminal() {
				return fmt.Errorf("cannot prompt for a variable value in non-interactive mode; pass --value")
			}

			tokenVal, err := helpers.ResolveProjectAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			varName, variant := args[0], ""
			if len(args) == 2 {
				variant = args[1]
			}
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

			result, err := api.CISetVariableVariant(ctx, tokenVal, orgID, api.CISetVariableVariantOptions{
				Name:        varName,
				Variant:     variant,
				Value:       varValue,
				Description: description,
				Repo:        repo,
				Environment: environment,
				Branch:      branch,
				Workflow:    workflow,
			})
			if err != nil {
				return fmt.Errorf("failed to set CI variable variant: %w", err)
			}

			fmt.Printf("Successfully set CI variable '%s' variant '%s'\n", varName, displayVariantName(result.Variant.Name))
			warnVariableVariantShadowed(ctx, tokenVal, orgID, result)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&value, "value", "", "Variable value (will prompt if not provided)")
	cmd.Flags().StringVar(&description, "description", "", "Description of the variable variant")
	cmd.Flags().StringArrayVar(&repo, "repo", nil, "Apply variant to a repository (repeatable, e.g. owner/repo)")
	cmd.Flags().StringArrayVar(&environment, "env", nil, "Apply variant to an environment (repeatable)")
	cmd.Flags().StringArrayVar(&branch, "branch", nil, "Apply variant to a branch (repeatable)")
	cmd.Flags().StringArrayVar(&workflow, "workflow", nil, "Apply variant to a workflow file (repeatable)")

	return cmd
}

func NewCmdVarsAdd() *cobra.Command {
	var (
		orgID       string
		token       string
		value       string
		repo        []string
		environment []string
		branch      []string
		workflow    []string
	)

	cmd := &cobra.Command{
		Use:   "add [VAR_NAME [variant] | KEY=VALUE ...]",
		Short: "Add one or more CI variables",
		Long: `Add variables that can be used in Depot CI workflows.

Supports three modes:
  1. Single variable with --value flag: depot ci vars add VAR_NAME --value "val"
  2. Single variable with interactive prompt: depot ci vars add VAR_NAME
  3. Bulk KEY=VALUE pairs: depot ci vars add FOO=bar BAZ=qux

The --value flag cannot be used with KEY=VALUE pairs.
Use --repo, --env, --branch, and --workflow to choose where the variant applies.
Without match flags, the variant applies to all workflow runs in the organization.`,
		Example: `  # Add an org-wide variable with interactive prompt
  depot ci vars add GITHUB_REPO

  # Add an org-wide variable with value from command line
  depot ci vars add MY_SERVICE_NAME --value "my_service"

  # Add a named variable variant
  depot ci vars add DEPLOY_ENV production --value "production"

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

			scope := variantScope(repo)
			variant := ""

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

				type variableInput struct {
					name  string
					value string
				}

				var variables []variableInput
				for _, arg := range args {
					parts := strings.SplitN(arg, "=", 2)
					if len(parts) != 2 || parts[0] == "" {
						return fmt.Errorf("invalid argument %q - expected KEY=VALUE format", arg)
					}
					variables = append(variables, variableInput{name: parts[0], value: parts[1]})
				}

				var results []api.CISetVariableVariantResult
				for _, variable := range variables {
					result, err := api.CISetVariableVariant(ctx, tokenVal, orgID, api.CISetVariableVariantOptions{
						Name:        variable.name,
						Variant:     variant,
						Value:       variable.value,
						Repo:        repo,
						Environment: environment,
						Branch:      branch,
						Workflow:    workflow,
					})
					if err != nil {
						return fmt.Errorf("failed to add CI variable '%s': %w", variable.name, err)
					}
					results = append(results, result)
				}

				for _, v := range variables {
					fmt.Printf("Successfully added CI variable '%s' variant '%s' (%s)\n", v.name, displayVariantName(variant), scope)
				}
				for _, result := range results {
					warnVariableVariantShadowed(ctx, tokenVal, orgID, result)
				}
				return nil
			}

			// Single mode: first arg is variable name, second optional arg is variant.
			if len(args) > 2 {
				return fmt.Errorf("too many arguments - did you mean to use KEY=VALUE format?")
			}

			varName := args[0]
			if len(args) == 2 {
				variant = args[1]
			}
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

			result, err := api.CISetVariableVariant(ctx, tokenVal, orgID, api.CISetVariableVariantOptions{
				Name:        varName,
				Variant:     variant,
				Value:       varValue,
				Repo:        repo,
				Environment: environment,
				Branch:      branch,
				Workflow:    workflow,
			})
			if err != nil {
				return fmt.Errorf("failed to add CI variable: %w", err)
			}

			fmt.Printf("Successfully added CI variable '%s' variant '%s' (%s)\n", varName, displayVariantName(variant), scope)
			warnVariableVariantShadowed(ctx, tokenVal, orgID, result)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&value, "value", "", "Variable value (will prompt if not provided)")
	cmd.Flags().StringArrayVar(&repo, "repo", nil, "Apply variant to a repository (repeatable, e.g. owner/repo)")
	cmd.Flags().StringArrayVar(&environment, "env", nil, "Apply variant to an environment (repeatable)")
	cmd.Flags().StringArrayVar(&branch, "branch", nil, "Apply variant to a branch (repeatable)")
	cmd.Flags().StringArrayVar(&workflow, "workflow", nil, "Apply variant to a workflow file (repeatable)")

	return cmd
}

func NewCmdVarsList() *cobra.Command {
	var (
		orgID       string
		token       string
		output      string
		repo        []string
		environment []string
		branch      []string
		workflow    []string
	)

	cmd := &cobra.Command{
		Use:   "list [<variable-name>]",
		Short: "List all CI variables",
		Long: `List CI variables and their variants.

Use --repo, --env, --branch, and --workflow to filter variants by matching
attributes. Passing a variable name lists one grouped variable.`,
		Aliases: []string{"ls"},
		Args:    cobra.MaximumNArgs(1),
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
			switch output {
			case "", "json":
			default:
				return fmt.Errorf("unsupported output %q (valid: json)", output)
			}
			if output == "json" && len(args) == 0 {
				if legacyRepo, ok := legacyListRepoSelector(repo, environment, branch, workflow); ok {
					variables, err := api.CIListVariables(ctx, tokenVal, orgID, legacyRepo)
					if err != nil {
						return fmt.Errorf("failed to list CI variables: %w", err)
					}
					return writeJSON(variables)
				}
			}

			// The selectors describe a job context. Route both the single-variable and list-all cases
			// through the same context-aware RPC so the server resolves each group for that context
			// (which variant wins, which are shadowed) and orders winners first. As with secrets, the
			// server decides which variants are relevant, so we deliberately do not re-filter here.
			hasContext := hasVariantSelectors(repo, environment, branch, workflow)
			opts := api.CIListVariableVariantsOptions{
				Repo:        repo,
				Environment: environment,
				Branch:      branch,
				Workflow:    workflow,
			}
			if len(args) == 1 {
				opts.Query = args[0]
			}

			result, err := api.CIListVariableVariants(ctx, tokenVal, orgID, opts)
			if err != nil {
				return fmt.Errorf("failed to list CI variables: %w", err)
			}

			if len(args) == 1 {
				// `query` is a substring match, so narrow to the exact variable and preserve the
				// direct-lookup behavior of reporting a missing variable as an error.
				filtered := result.Variables[:0]
				for i := range result.Variables {
					if strings.EqualFold(result.Variables[i].Name, args[0]) {
						filtered = append(filtered, result.Variables[i])
					}
				}
				result.Variables = filtered
				if len(result.Variables) == 0 {
					return fmt.Errorf("CI variable %q not found", args[0])
				}
			} else if hasContext {
				// Drop variables with no variant relevant to the context so the listing stays focused.
				filtered := result.Variables[:0]
				for i := range result.Variables {
					if len(result.Variables[i].Variants) > 0 {
						filtered = append(filtered, result.Variables[i])
					}
				}
				result.Variables = filtered
			}

			if output == "json" {
				return writeJSON(result)
			}

			if len(result.Variables) == 0 {
				fmt.Println("No CI variables found.")
				return nil
			}

			printVariableVariantsTable(result.Variables, hasContext)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&output, "output", "", "Output format (json)")
	cmd.Flags().StringArrayVar(&repo, "repo", nil, "Filter variants by repository (repeatable, e.g. owner/repo)")
	cmd.Flags().StringArrayVar(&environment, "env", nil, "Filter variants by environment (repeatable)")
	cmd.Flags().StringArrayVar(&branch, "branch", nil, "Filter variants by branch (repeatable)")
	cmd.Flags().StringArrayVar(&workflow, "workflow", nil, "Filter variants by workflow file (repeatable)")

	return cmd
}

// printVariableVariantsTable renders variables and their variants. Unlike secrets, variable values are
// not sensitive, so the VALUE and DESCRIPTION columns are kept. When hasContext is true the request
// carried a job context, so each variant shows a STATUS that reveals which one wins and which are
// silently overridden; STATUS is the trailing column because its labels are the widest. Variables
// report resolution only at the group level, so the per-row status is derived from the server's
// winner-first ordering (see variableVariantRowResolution).
func printVariableVariantsTable(variables []api.CIVariableGroup, hasContext bool) {
	const (
		nameWidth    = 28
		varWidth     = 16
		valueWidth   = 28
		scopeWidth   = 28
		descWidth    = 24
		updatedWidth = 20
	)

	if hasContext {
		fmt.Printf("%-*s %-*s %-*s %-*s %-*s %-*s %s\n", nameWidth, "NAME", varWidth, "VARIANT", valueWidth, "VALUE", scopeWidth, "SCOPE", descWidth, "DESCRIPTION", updatedWidth, "UPDATED", "STATUS")
		fmt.Printf("%-*s %-*s %-*s %-*s %-*s %-*s %s\n", nameWidth, strings.Repeat("-", nameWidth), varWidth, strings.Repeat("-", varWidth), valueWidth, strings.Repeat("-", valueWidth), scopeWidth, strings.Repeat("-", scopeWidth), descWidth, strings.Repeat("-", descWidth), updatedWidth, strings.Repeat("-", updatedWidth), strings.Repeat("-", 12))
	} else {
		fmt.Printf("%-*s %-*s %-*s %-*s %-*s %s\n", nameWidth, "NAME", varWidth, "VARIANT", valueWidth, "VALUE", scopeWidth, "SCOPE", descWidth, "DESCRIPTION", "UPDATED")
		fmt.Printf("%-*s %-*s %-*s %-*s %-*s %s\n", nameWidth, strings.Repeat("-", nameWidth), varWidth, strings.Repeat("-", varWidth), valueWidth, strings.Repeat("-", valueWidth), scopeWidth, strings.Repeat("-", scopeWidth), descWidth, strings.Repeat("-", descWidth), strings.Repeat("-", updatedWidth))
	}

	multipleVariants := false
	for _, variable := range variables {
		if variable.VariantCount > 1 || len(variable.Variants) > 1 {
			multipleVariants = true
		}
		if len(variable.Variants) == 0 {
			if hasContext {
				fmt.Printf("%-*s %-*s %-*s %-*s %-*s %-*s %s\n", nameWidth, truncateForTable(variable.Name, nameWidth), varWidth, "-", valueWidth, "-", scopeWidth, "-", descWidth, "-", updatedWidth, variable.LastModified, "-")
			} else {
				fmt.Printf("%-*s %-*s %-*s %-*s %-*s %s\n", nameWidth, truncateForTable(variable.Name, nameWidth), varWidth, "-", valueWidth, "-", scopeWidth, "-", descWidth, "-", variable.LastModified)
			}
			continue
		}
		for index, variant := range variable.Variants {
			scope := truncateForTable(formatVariantAttributes(variant.Attributes), scopeWidth)
			if hasContext {
				fmt.Printf("%-*s %-*s %-*s %-*s %-*s %-*s %s\n",
					nameWidth, truncateForTable(variable.Name, nameWidth),
					varWidth, truncateForTable(displayVariantName(variant.Name), varWidth),
					valueWidth, truncateForTable(variant.Value, valueWidth),
					scopeWidth, scope,
					descWidth, truncateForTable(variant.Description, descWidth),
					updatedWidth, variant.LastModified,
					variantStatusLabel(variableVariantRowResolution(variable.Resolution, index)),
				)
			} else {
				fmt.Printf("%-*s %-*s %-*s %-*s %-*s %s\n",
					nameWidth, truncateForTable(variable.Name, nameWidth),
					varWidth, truncateForTable(displayVariantName(variant.Name), varWidth),
					valueWidth, truncateForTable(variant.Value, valueWidth),
					scopeWidth, scope,
					descWidth, truncateForTable(variant.Description, descWidth),
					variant.LastModified,
				)
			}
		}
	}

	fmt.Println()
	if hasContext {
		fmt.Println("STATUS: active (wins) = wins this context · shadowed = overridden by a more specific variant · may win = could win with more context")
	} else if multipleVariants {
		fmt.Println("Some variables have more than one variant. Pass --repo, --env, --branch, or --workflow to see which one wins.")
	}
}

func NewCmdVarsRemove() *cobra.Command {
	var (
		orgID       string
		token       string
		force       bool
		repo        []string
		environment []string
		branch      []string
		workflow    []string
		variant     string
		all         bool
	)

	cmd := &cobra.Command{
		Use:   "remove <variable-name> [<variable-name>...]",
		Short: "Remove one or more CI variables",
		Long: `Remove one or more CI variables.

By default, positional arguments are treated as variable names and the command
removes the whole variable with every variant under it. Use selector flags or
--variant to remove one matching variant. --all makes whole-variable removal
explicit and cannot be combined with selector flags or --variant.`,
		Example: `  # Remove an org-wide variable
  depot ci vars remove GITHUB_REPO

  # Remove a repo-specific variant
  depot ci vars remove GITHUB_REPO --repo owner/repo

  # Remove a named variant
  depot ci vars remove GITHUB_REPO --variant production

  # Remove every variant for a variable
  depot ci vars remove GITHUB_REPO --all

  # Remove variables without confirmation prompt
  depot ci vars remove GITHUB_REPO MY_SERVICE_NAME --force`,
		Aliases: []string{"rm"},
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			names := args
			selectsVariant := variant != "" || hasVariantSelectors(repo, environment, branch, workflow)
			if all && selectsVariant {
				return fmt.Errorf("--all cannot be used with --variant, --repo, --env, --branch, or --workflow")
			}
			removeGroups := all || !selectsVariant

			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			if !force {
				namesLabel := strings.Join(names, ", ")
				var target string
				if removeGroups {
					target = "CI variable(s) and all variants"
				} else if variant != "" {
					target = fmt.Sprintf("variant %q for CI variable(s)", variant)
				} else {
					target = "selected CI variable variant(s)"
				}
				prompt := fmt.Sprintf("Are you sure you want to remove %s %s? (y/N): ", target, namesLabel)
				y, err := helpers.PromptForYN(prompt)
				if err != nil {
					return fmt.Errorf("failed to read confirmation: %w", err)
				} else if !y {
					return nil
				}
			}

			for _, varName := range names {
				if removeGroups {
					if err := api.CIDeleteVariableGroup(ctx, tokenVal, orgID, varName); err != nil {
						return fmt.Errorf("failed to remove CI variable '%s': %w", varName, err)
					}
					fmt.Printf("Successfully removed CI variable '%s' and all variants\n", varName)
					continue
				}

				group, err := api.CIGetVariableVariantGroup(ctx, tokenVal, orgID, varName)
				if err != nil {
					return fmt.Errorf("failed to get CI variable '%s': %w", varName, err)
				}

				matches, err := resolveVariableVariant(group, variant, repo, environment, branch, workflow)
				if err != nil {
					return err
				}
				if len(matches) == 0 {
					return fmt.Errorf("no matching variant found for CI variable '%s'", varName)
				}
				if len(matches) > 1 {
					return fmt.Errorf("CI variable '%s' has multiple matching variants; pass --variant or add selector flags", varName)
				}

				if _, err := api.CIDeleteVariableVariant(ctx, tokenVal, orgID, matches[0].ID); err != nil {
					return fmt.Errorf("failed to remove CI variable '%s' variant '%s': %w", varName, matches[0].Name, err)
				}
				fmt.Printf("Successfully removed CI variable '%s' variant '%s'\n", varName, displayVariantName(matches[0].Name))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	cmd.Flags().StringArrayVar(&repo, "repo", nil, "Select variant matching a repository (repeatable, e.g. owner/repo)")
	cmd.Flags().StringArrayVar(&environment, "env", nil, "Select variant matching an environment (repeatable)")
	cmd.Flags().StringArrayVar(&branch, "branch", nil, "Select variant matching a branch (repeatable)")
	cmd.Flags().StringArrayVar(&workflow, "workflow", nil, "Select variant matching a workflow file (repeatable)")
	cmd.Flags().StringVar(&variant, "variant", "", "Select variant by name")
	cmd.Flags().BoolVar(&all, "all", false, "Remove the variable and all variants")

	return cmd
}
