package ci

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdSecrets() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage CI secrets",
		Long:  "Manage secrets for Depot CI workflows.",
		Example: `  # Add a new secret
  depot ci secrets add GITHUB_TOKEN
  printf '%s' "$MY_API_KEY" | depot ci secrets add MY_API_KEY

  # Set a named secret variant
  printf '%s' "$MY_API_KEY" | depot ci secrets set MY_API_KEY production --repo owner/repo --env production

  # Add multiple secrets at once (legacy syntax)
  depot ci secrets add FOO=bar BAZ=qux

  # Import secrets from a .env file
  depot ci secrets bulk --file .env --repo owner/repo

  # Add a repo-specific secret from piped input
  printf '%s' "$MY_API_KEY" | depot ci secrets add MY_API_KEY --repo owner/repo

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

	cmd.AddCommand(NewCmdSecretsSet())
	cmd.AddCommand(NewCmdSecretsAdd())
	cmd.AddCommand(NewCmdSecretsBulk())
	cmd.AddCommand(NewCmdSecretsGet())
	cmd.AddCommand(NewCmdSecretsList())
	cmd.AddCommand(NewCmdSecretsRemove())

	return cmd
}

func NewCmdSecretsSet() *cobra.Command {
	var (
		orgID       string
		token       string
		description string
		repo        []string
		environment []string
		branch      []string
		workflow    []string
	)

	cmd := &cobra.Command{
		Use:   "set <secret-name> [variant]",
		Short: "Create or update a CI secret variant",
		Long: `Create or update a CI secret variant.

Variants let one secret name have different values for matching repositories,
environments, branches, or workflows. When variant is omitted, the variant is
named "default".`,
		Example: `  # Set the default variant
  depot ci secrets set MY_API_KEY

  # Set a named variant from piped input
  printf '%s' "$MY_API_KEY" | depot ci secrets set MY_API_KEY production

  # Set a variant that only applies to matching workflow runs from piped input
  printf '%s' "$MY_API_KEY" | depot ci secrets set MY_API_KEY production --repo owner/repo --env production --branch main --workflow deploy.yml

  # Set a variant that applies to multiple branches
  printf '%s' "$MY_API_KEY" | depot ci secrets set MY_API_KEY release --repo owner/repo --branch main --branch 'release/*'`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			tokenVal, err := helpers.ResolveProjectAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			secretName, variant := args[0], ""
			if len(args) == 2 {
				variant = args[1]
			}
			if secretName == "" {
				return fmt.Errorf("secret name cannot be empty")
			}

			secretValue, err := helpers.SecretValueFromInput(fmt.Sprintf("Enter value for secret '%s': ", secretName))
			if err != nil {
				return fmt.Errorf("failed to read secret value: %w", err)
			}

			result, err := api.CISetSecretVariant(ctx, tokenVal, orgID, api.CISetSecretVariantOptions{
				Name:        secretName,
				Variant:     variant,
				Value:       secretValue,
				Description: description,
				Repo:        repo,
				Environment: environment,
				Branch:      branch,
				Workflow:    workflow,
			})
			if err != nil {
				return fmt.Errorf("failed to set secret variant: %w", err)
			}

			fmt.Printf("Successfully set CI secret '%s' variant '%s'\n", secretName, displayVariantName(result.Variant.Name))
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&description, "description", "", "Description of the secret variant")
	cmd.Flags().StringArrayVar(&repo, "repo", nil, "Apply variant to a repository (repeatable, e.g. owner/repo)")
	cmd.Flags().StringArrayVar(&environment, "env", nil, "Apply variant to an environment (repeatable)")
	cmd.Flags().StringArrayVar(&branch, "branch", nil, "Apply variant to a branch (repeatable)")
	cmd.Flags().StringArrayVar(&workflow, "workflow", nil, "Apply variant to a workflow file (repeatable)")

	return cmd
}

type secretInput struct {
	name  string
	value string
}

func NewCmdSecretsAdd() *cobra.Command {
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
		Use:   "add [SECRET_NAME [variant] | KEY=VALUE ...]",
		Short: "Add one or more CI secrets",
		Long: `Add secrets that can be used in Depot CI workflows.

Supports three modes:
  1. Single secret with interactive prompt: depot ci secrets add SECRET_NAME
  2. Single secret from stdin: printf '%s' "$SECRET_VALUE" | depot ci secrets add SECRET_NAME
  3. Bulk KEY=VALUE pairs: depot ci secrets add FOO=bar BAZ=qux

The --description flag cannot be used with KEY=VALUE pairs.
Use --repo, --env, --branch, and --workflow to choose where the variant applies.
Without match flags, the variant applies to all workflow runs in the organization.`,
		Example: `  # Add an org-wide secret with interactive prompt
  depot ci secrets add GITHUB_TOKEN

  # Add a secret from piped input
  printf '%s' "$MY_API_KEY" | depot ci secrets add MY_API_KEY

  # Add a named variant from piped input
  printf '%s' "$MY_API_KEY" | depot ci secrets add MY_API_KEY production

  # Add multiple secrets at once (legacy syntax)
  depot ci secrets add FOO=bar BAZ=qux

  # Add a repo-specific secret from piped input
  printf '%s' "$DATABASE_URL" | depot ci secrets add DATABASE_URL --repo owner/repo

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
				if description != "" {
					return fmt.Errorf("cannot use --description with KEY=VALUE arguments")
				}

				var secrets []secretInput
				for _, arg := range args {
					parts := strings.SplitN(arg, "=", 2)
					if len(parts) != 2 || parts[0] == "" {
						return fmt.Errorf("invalid argument %q - expected KEY=VALUE format", arg)
					}
					secrets = append(secrets, secretInput{name: parts[0], value: parts[1]})
				}

				for _, secret := range secrets {
					_, err := api.CISetSecretVariant(ctx, tokenVal, orgID, api.CISetSecretVariantOptions{
						Name:        secret.name,
						Variant:     variant,
						Value:       secret.value,
						Repo:        repo,
						Environment: environment,
						Branch:      branch,
						Workflow:    workflow,
					})
					if err != nil {
						return fmt.Errorf("failed to add secret '%s': %w", secret.name, err)
					}
				}

				for _, s := range secrets {
					printSecretAddSuccess(s.name, variant, scope)
				}
				return nil
			}

			// Single mode: first arg is secret name, second optional arg is variant.
			if len(args) > 2 {
				return fmt.Errorf("too many arguments - did you mean to use KEY=VALUE format?")
			}

			secretName := args[0]
			if len(args) == 2 {
				variant = args[1]
			}
			if secretName == "" {
				return fmt.Errorf("secret name cannot be empty")
			}

			secretValue := value
			if secretValue == "" {
				secretValue, err = helpers.SecretValueFromInput(fmt.Sprintf("Enter value for secret '%s': ", secretName))
				if err != nil {
					return fmt.Errorf("failed to read secret value: %w", err)
				}
			}

			_, err = api.CISetSecretVariant(ctx, tokenVal, orgID, api.CISetSecretVariantOptions{
				Name:        secretName,
				Variant:     variant,
				Value:       secretValue,
				Description: description,
				Repo:        repo,
				Environment: environment,
				Branch:      branch,
				Workflow:    workflow,
			})
			if err != nil {
				return fmt.Errorf("failed to add secret: %w", err)
			}

			printSecretAddSuccess(secretName, variant, scope)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&value, "value", "", "Secret value (deprecated; prefer stdin)")
	cmd.Flags().StringVar(&description, "description", "", "Description of the secret variant")
	cmd.Flags().StringArrayVar(&repo, "repo", nil, "Apply variant to a repository (repeatable, e.g. owner/repo)")
	cmd.Flags().StringArrayVar(&environment, "env", nil, "Apply variant to an environment (repeatable)")
	cmd.Flags().StringArrayVar(&branch, "branch", nil, "Apply variant to a branch (repeatable)")
	cmd.Flags().StringArrayVar(&workflow, "workflow", nil, "Apply variant to a workflow file (repeatable)")
	_ = cmd.Flags().MarkHidden("value")

	return cmd
}

func NewCmdSecretsBulk() *cobra.Command {
	var (
		orgID       string
		token       string
		file        string
		repo        []string
		environment []string
		branch      []string
		workflow    []string
		fromStdin   bool
	)

	cmd := &cobra.Command{
		Use:   "bulk [variant]",
		Short: "Import CI secrets from a dotenv file or stdin",
		Long: `Import CI secrets from a dotenv file or stdin.

Input is parsed as dotenv KEY=VALUE entries. Blank lines and comments are
ignored by the dotenv parser. The same variant and match flags apply to every
secret in the input.`,
		Example: `  # Import secrets from a .env file
  depot ci secrets bulk --file .env --repo owner/repo

  # Import secrets from stdin
  cat .env | depot ci secrets bulk --from-stdin --repo owner/repo

  # Import a production variant with multiple branch matches
  depot ci secrets bulk production --file .env --repo owner/repo --branch main --branch 'release/*'`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			var variant string
			if len(args) == 1 {
				variant = args[0]
			}
			if file != "" && fromStdin {
				return fmt.Errorf("--file and --from-stdin are mutually exclusive")
			}
			if file == "" && !fromStdin {
				return fmt.Errorf("missing input source; pass --file or --from-stdin")
			}

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			tokenVal, err := helpers.ResolveProjectAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			var content []byte
			if fromStdin {
				content, err = io.ReadAll(os.Stdin)
			} else {
				content, err = os.ReadFile(file)
			}
			if err != nil {
				return fmt.Errorf("failed to read secrets input: %w", err)
			}

			secrets, err := parseSecretBulkEnv(content)
			if err != nil {
				return err
			}
			if len(secrets) == 0 {
				return fmt.Errorf("no secrets found in input")
			}

			for _, secret := range secrets {
				_, err := api.CISetSecretVariant(ctx, tokenVal, orgID, api.CISetSecretVariantOptions{
					Name:        secret.name,
					Variant:     variant,
					Value:       secret.value,
					Repo:        repo,
					Environment: environment,
					Branch:      branch,
					Workflow:    workflow,
				})
				if err != nil {
					return fmt.Errorf("failed to import secret '%s': %w", secret.name, err)
				}
			}

			scope := variantScope(repo)
			for _, secret := range secrets {
				printSecretAddSuccess(secret.name, variant, scope)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&file, "file", "", "Read dotenv input from file")
	cmd.Flags().StringArrayVar(&repo, "repo", nil, "Apply variant to a repository (repeatable, e.g. owner/repo)")
	cmd.Flags().StringArrayVar(&environment, "env", nil, "Apply variant to an environment (repeatable)")
	cmd.Flags().StringArrayVar(&branch, "branch", nil, "Apply variant to a branch (repeatable)")
	cmd.Flags().StringArrayVar(&workflow, "workflow", nil, "Apply variant to a workflow file (repeatable)")
	cmd.Flags().BoolVar(&fromStdin, "from-stdin", false, "Read dotenv input from stdin")

	return cmd
}

func parseSecretBulkEnv(content []byte) ([]secretInput, error) {
	envs, err := dotenv.UnmarshalBytesWithLookup(content, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to parse dotenv input: %w", err)
	}

	names := make([]string, 0, len(envs))
	for name := range envs {
		names = append(names, name)
	}
	sort.Strings(names)

	secrets := make([]secretInput, 0, len(names))
	for _, name := range names {
		secrets = append(secrets, secretInput{name: name, value: envs[name]})
	}
	return secrets, nil
}

func printSecretAddSuccess(name, variant, scope string) {
	if variant == "" {
		fmt.Printf("Successfully added CI secret '%s' (%s)\n", name, scope)
		return
	}
	fmt.Printf("Successfully added CI secret '%s' variant '%s' (%s)\n", name, displayVariantName(variant), scope)
}

func NewCmdSecretsGet() *cobra.Command {
	var (
		orgID       string
		token       string
		output      string
		variantID   string
		repo        []string
		environment []string
		branch      []string
		workflow    []string
	)

	cmd := &cobra.Command{
		Use:   "get [<secret-name> [variant]]",
		Short: "Show one CI secret variant",
		Long: `Show one CI secret variant with full, untruncated attributes.

Use --variant-id to fetch a specific variant directly, or pass a secret name
with an optional variant and match flags to resolve one variant.`,
		Example: `  # Show a variant by secret and variant name
  depot ci secrets get MY_API_KEY production

  # Disambiguate variants with match flags
  depot ci secrets get MY_API_KEY production --repo owner/repo --branch main

  # Show a variant by ID
  depot ci secrets get --variant-id variant-id

  # Show JSON
  depot ci secrets get MY_API_KEY production --output json`,
		Args: cobra.MaximumNArgs(2),
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

			var secretName string
			var resolved api.CISecretVariant
			if variantID != "" {
				if len(args) > 0 {
					return fmt.Errorf("cannot pass a secret name with --variant-id")
				}
				resolved, err = api.CIGetSecretVariant(ctx, tokenVal, orgID, variantID)
				if err != nil {
					return fmt.Errorf("failed to get secret variant: %w", err)
				}
			} else {
				if len(args) == 0 {
					return fmt.Errorf("missing secret name or --variant-id")
				}
				secretName = args[0]
				variant := ""
				if len(args) == 2 {
					variant = args[1]
				}
				group, err := api.CIGetSecretVariantGroup(ctx, tokenVal, orgID, secretName)
				if err != nil {
					return fmt.Errorf("failed to get secret: %w", err)
				}

				matches, err := resolveSecretVariant(group, variant, repo, environment, branch, workflow)
				if err != nil {
					return err
				}
				if len(matches) == 0 {
					return fmt.Errorf("no matching variant found for secret '%s'", secretName)
				}
				if len(matches) > 1 {
					return fmt.Errorf("secret '%s' has multiple matching variants; use --variant-id or add match flags", secretName)
				}
				resolved = matches[0]
			}

			if output == "json" {
				return writeJSON(resolved)
			}

			printSecretVariantDetail(secretName, resolved)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&output, "output", "", "Output format (json)")
	cmd.Flags().StringVar(&variantID, "variant-id", "", "Secret variant ID")
	cmd.Flags().StringArrayVar(&repo, "repo", nil, "Select variant matching a repository (repeatable, e.g. owner/repo)")
	cmd.Flags().StringArrayVar(&environment, "env", nil, "Select variant matching an environment (repeatable)")
	cmd.Flags().StringArrayVar(&branch, "branch", nil, "Select variant matching a branch (repeatable)")
	cmd.Flags().StringArrayVar(&workflow, "workflow", nil, "Select variant matching a workflow file (repeatable)")

	return cmd
}

func printSecretVariantDetail(secretName string, variant api.CISecretVariant) {
	if secretName == "" {
		secretName = variant.SecretID
	}
	fmt.Printf("Name:        %s\n", secretName)
	fmt.Printf("Variant:     %s\n", displayVariantName(variant.Name))
	fmt.Printf("ID:          %s\n", variant.ID)
	if variant.SecretID != "" {
		fmt.Printf("Secret ID:   %s\n", variant.SecretID)
	}
	if variant.Description != "" {
		fmt.Printf("Description: %s\n", variant.Description)
	}
	if variant.LastModified != "" {
		fmt.Printf("Updated:     %s\n", variant.LastModified)
	}
	if variant.ValueGroupIndex != nil {
		fmt.Printf("Value Group: %d\n", *variant.ValueGroupIndex)
	}
	fmt.Println()
	fmt.Println("Attributes:")
	if len(variant.Attributes) == 0 {
		fmt.Println("  all")
		return
	}
	for _, attr := range variant.Attributes {
		fmt.Printf("  %s=%s\n", attr.Key, attr.Value)
	}
}

func NewCmdSecretsList() *cobra.Command {
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
		Use:   "list [<secret-name>]",
		Short: "List all CI secrets",
		Long: `List CI secrets and their variants.

Use --repo, --env, --branch, and --workflow to filter variants by matching
attributes. Passing a secret name lists one grouped secret.`,
		Example: `  # List org-wide secrets
  depot ci secrets list

  # List variants matching repositories or branches
  depot ci secrets list --repo owner/repo --repo owner/other --branch main

  # List one secret with its variants
  depot ci secrets list MY_API_KEY

  # List secrets in JSON format
  depot ci secrets list --output json`,
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

			var result api.CIListSecretVariantsResult
			if len(args) == 1 {
				secret, err := api.CIGetSecretVariantGroup(ctx, tokenVal, orgID, args[0])
				if err != nil {
					return fmt.Errorf("failed to get secret: %w", err)
				}
				result.Secrets = []api.CISecretGroup{filterSecretVariants(secret, repo, environment, branch, workflow)}
			} else {
				var err error
				result, err = api.CIListSecretVariants(ctx, tokenVal, orgID, api.CIListSecretVariantsOptions{
					Repo:        repo,
					Environment: environment,
					Branch:      branch,
					Workflow:    workflow,
				})
				if err != nil {
					return fmt.Errorf("failed to list secrets: %w", err)
				}
			}

			if output == "json" {
				return writeJSON(result)
			}

			if len(result.Secrets) == 0 {
				fmt.Println("No secrets found.")
				return nil
			}

			fmt.Printf("%-30s %-18s %-38s %s\n", "NAME", "VARIANT", "DESCRIPTION", "UPDATED")
			fmt.Printf("%-30s %-18s %-38s %s\n", strings.Repeat("-", 30), strings.Repeat("-", 18), strings.Repeat("-", 38), strings.Repeat("-", 20))
			for _, secret := range result.Secrets {
				if len(secret.Variants) == 0 {
					fmt.Printf("%-30s %-18s %-38s %s\n", truncateForTable(secret.Name, 30), "-", "-", secret.LastModified)
					continue
				}
				for _, variant := range secret.Variants {
					fmt.Printf("%-30s %-18s %-38s %s\n",
						truncateForTable(secret.Name, 30),
						truncateForTable(displayVariantName(variant.Name), 18),
						truncateForTable(variant.Description, 38),
						variant.LastModified,
					)
				}
			}

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

func NewCmdSecretsRemove() *cobra.Command {
	var (
		orgID       string
		token       string
		force       bool
		repo        []string
		environment []string
		branch      []string
		workflow    []string
		all         bool
	)

	cmd := &cobra.Command{
		Use:   "remove <secret-name> [variant]",
		Short: "Remove one or more CI secrets",
		Long: `Remove one or more CI secret variants.

By default, removal only succeeds when the target secret has one unambiguous
variant. Pass a variant name to delete a named variant, or use --all to delete the whole
secret and every variant under it.`,
		Example: `  # Remove an org-wide secret
  depot ci secrets remove GITHUB_TOKEN

  # Remove a named variant
  depot ci secrets remove GITHUB_TOKEN production

  # Remove every variant for a secret
  depot ci secrets remove GITHUB_TOKEN --all

  # Remove multiple secrets without confirmation prompt
  depot ci secrets remove GITHUB_TOKEN MY_API_KEY --all --force`,
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

			var variant string
			names := args
			if all {
				variant = ""
			} else {
				if len(args) > 2 {
					return fmt.Errorf("too many arguments; pass one secret and optional variant, or use --all to remove multiple secrets")
				}
				if len(args) == 2 {
					variant = args[1]
				}
				names = args[:1]
			}

			if !force {
				namesLabel := strings.Join(names, ", ")
				target := "selected CI secret variant(s)"
				if all {
					target = "all variants for CI secret(s)"
				} else if variant != "" {
					target = fmt.Sprintf("variant %q for CI secret(s)", variant)
				}
				prompt := fmt.Sprintf("Are you sure you want to remove %s %s? (y/N): ", target, namesLabel)
				y, err := helpers.PromptForYN(prompt)
				if err != nil {
					return fmt.Errorf("failed to read confirmation: %w", err)
				} else if !y {
					return nil
				}
			}

			for _, secretName := range names {
				if all {
					if err := api.CIDeleteSecretGroup(ctx, tokenVal, orgID, secretName); err != nil {
						return fmt.Errorf("failed to remove secret '%s': %w", secretName, err)
					}
					fmt.Printf("Successfully removed CI secret '%s' and all variants\n", secretName)
					continue
				}

				group, err := api.CIGetSecretVariantGroup(ctx, tokenVal, orgID, secretName)
				if err != nil {
					return fmt.Errorf("failed to get secret '%s': %w", secretName, err)
				}

				matches, err := resolveSecretVariant(group, variant, repo, environment, branch, workflow)
				if err != nil {
					return err
				}
				if len(matches) == 0 {
					return fmt.Errorf("no matching variant found for secret '%s'", secretName)
				}
				if len(matches) > 1 {
					return fmt.Errorf("secret '%s' has multiple matching variants; pass a variant or use --all", secretName)
				}

				if _, err := api.CIDeleteSecretVariant(ctx, tokenVal, orgID, matches[0].ID); err != nil {
					return fmt.Errorf("failed to remove secret '%s' variant '%s': %w", secretName, matches[0].Name, err)
				}
				fmt.Printf("Successfully removed CI secret '%s' variant '%s'\n", secretName, displayVariantName(matches[0].Name))
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
	cmd.Flags().BoolVar(&all, "all", false, "Remove the secret and all variants")

	return cmd
}
