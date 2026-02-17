package ci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/ci/compat"
	"github.com/depot/cli/pkg/ci/migrate"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

type migrateOptions struct {
	orgID     string
	token     string
	yes       bool
	secrets   []string
	overwrite bool
	dir       string
	stdout    io.Writer
}

func NewCmdMigrate() *cobra.Command {
	var opts migrateOptions

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate GitHub Actions workflows to Depot CI",
		Long:  "Interactive wizard to migrate your GitHub Actions CI configuration to Depot CI.",
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			runOpts.dir = "."
			runOpts.stdout = os.Stdout
			return runMigrate(cmd.Context(), runOpts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	flags.StringVar(&opts.token, "token", "", "Depot API token")
	flags.BoolVar(&opts.yes, "yes", false, "Run in non-interactive mode")
	flags.StringArrayVar(&opts.secrets, "secret", nil, "CI secret assignment in KEY=VALUE format (repeatable)")
	flags.BoolVar(&opts.overwrite, "overwrite", false, "Overwrite existing .depot/ directory")

	return cmd
}

func runMigrate(ctx context.Context, opts migrateOptions) error {
	workDir := opts.dir
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}

	out := opts.stdout
	if out == nil {
		out = os.Stdout
	}

	githubDir := filepath.Join(workDir, ".github")
	workflowsDir := filepath.Join(githubDir, "workflows")

	if stat, err := os.Stat(githubDir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no .github directory found in %s", workDir)
		}
		return fmt.Errorf("failed to inspect .github directory: %w", err)
	} else if !stat.IsDir() {
		return fmt.Errorf(".github exists but is not a directory")
	}

	if stat, err := os.Stat(workflowsDir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no .github/workflows directory found in %s", workDir)
		}
		return fmt.Errorf("failed to inspect .github/workflows directory: %w", err)
	} else if !stat.IsDir() {
		return fmt.Errorf(".github/workflows exists but is not a directory")
	}

	workflows, err := migrate.ParseWorkflowDir(workflowsDir)
	if err != nil {
		return fmt.Errorf("failed to parse workflow files: %w", err)
	}
	if len(workflows) == 0 {
		return fmt.Errorf("no workflow files found in .github/workflows")
	}

	selectedWorkflows := workflows

	fmt.Fprintf(out, "Found %d workflow(s) in .github/workflows\n", len(workflows))
	for _, workflow := range workflows {
		report := compat.AnalyzeWorkflow(workflow)
		summary := compat.SummarizeReport(report)
		triggers := "none"
		if len(workflow.Triggers) > 0 {
			triggers = strings.Join(workflow.Triggers, ", ")
		}
		critical := ""
		if compat.HasCriticalIssues(report) {
			critical = " [critical issues]"
		}
		fmt.Fprintf(out, "- %s (%s): %s%s\n", filepath.Base(workflow.Path), triggers, summary, critical)
	}

	if !opts.yes {
		if !helpers.IsTerminal() {
			return fmt.Errorf("interactive mode requires a terminal; rerun with --yes")
		}

		huhOptions := make([]huh.Option[string], 0, len(workflows))
		for _, workflow := range workflows {
			triggerLabel := "none"
			if len(workflow.Triggers) > 0 {
				triggerLabel = strings.Join(workflow.Triggers, ", ")
			}
			label := fmt.Sprintf("%s - %s", filepath.Base(workflow.Path), triggerLabel)
			huhOptions = append(huhOptions, huh.NewOption(label, workflow.Path))
		}

		var selected []string
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewMultiSelect[string]().
					Title("Select workflows to migrate").
					Options(huhOptions...).
					Value(&selected),
			),
		)

		if err := form.Run(); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				fmt.Fprintln(out, "Migration cancelled.")
				return nil
			}
			return fmt.Errorf("failed to select workflows: %w", err)
		}

		if len(selected) == 0 {
			fmt.Fprintln(out, "No workflows selected. Nothing to migrate.")
			return nil
		}

		selectedSet := make(map[string]struct{}, len(selected))
		for _, path := range selected {
			selectedSet[path] = struct{}{}
		}

		selectedWorkflows = make([]*migrate.WorkflowFile, 0, len(selected))
		for _, workflow := range workflows {
			if _, ok := selectedSet[workflow.Path]; ok {
				selectedWorkflows = append(selectedWorkflows, workflow)
			}
		}
	}

	copyMode := migrate.CopyModeError
	depotDir := filepath.Join(workDir, ".depot")
	if depotInfo, err := os.Stat(depotDir); err == nil {
		if !depotInfo.IsDir() {
			return fmt.Errorf(".depot exists but is not a directory")
		}

		if opts.overwrite {
			copyMode = migrate.CopyModeOverwrite
		} else if opts.yes {
			return fmt.Errorf(".depot directory already exists; rerun with --overwrite to continue")
		} else {
			confirmOverwrite := false
			err := huh.NewConfirm().
				Title(".depot directory already exists. Overwrite matching files?").
				Affirmative("Yes").
				Negative("No").
				Value(&confirmOverwrite).
				Run()
			if err != nil {
				if errors.Is(err, huh.ErrUserAborted) {
					fmt.Fprintln(out, "Migration cancelled.")
					return nil
				}
				return fmt.Errorf("failed to confirm overwrite: %w", err)
			}
			if !confirmOverwrite {
				fmt.Fprintln(out, "Migration cancelled.")
				return nil
			}
			copyMode = migrate.CopyModeOverwrite
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect .depot directory: %w", err)
	}

	copyResult, err := migrate.CopyGitHubToDepot(workDir, []string{"workflows", "actions"}, copyMode)
	if err != nil {
		return fmt.Errorf("failed to copy GitHub CI files: %w", err)
	}

	detectedSecrets, err := detectSecretsFromWorkflows(selectedWorkflows)
	if err != nil {
		return fmt.Errorf("failed to detect secrets: %w", err)
	}

	configuredSecrets := make([]string, 0)
	warnings := append([]string{}, copyResult.Warnings...)

	secretAssignments, err := parseSecretAssignments(opts.secrets)
	if err != nil {
		return err
	}

	if opts.yes {
		if len(secretAssignments) > 0 {
			orgID, token, err := resolveMigrationAuth(ctx, opts)
			if err != nil {
				return err
			}

			secretNames := make([]string, 0, len(secretAssignments))
			for name := range secretAssignments {
				secretNames = append(secretNames, name)
			}
			sort.Strings(secretNames)

			for _, name := range secretNames {
				if err := api.CIAddSecret(ctx, token, orgID, name, secretAssignments[name]); err != nil {
					return fmt.Errorf("failed to configure secret %s: %w", name, err)
				}
				configuredSecrets = append(configuredSecrets, name)
			}
		}

		for _, name := range detectedSecrets {
			if _, ok := secretAssignments[name]; !ok {
				warnings = append(warnings, fmt.Sprintf("detected secret %s is not configured", name))
			}
		}
	} else if len(detectedSecrets) > 0 {
		orgID, token, err := resolveMigrationAuth(ctx, opts)
		if err != nil {
			return err
		}

		for _, name := range detectedSecrets {
			value, err := promptForCISecret(fmt.Sprintf("Enter value for secret '%s' (leave empty to skip): ", name))
			if err != nil {
				return fmt.Errorf("failed to read value for secret %s: %w", name, err)
			}
			if strings.TrimSpace(value) == "" {
				warnings = append(warnings, fmt.Sprintf("secret %s was skipped", name))
				continue
			}

			if err := api.CIAddSecret(ctx, token, orgID, name, value); err != nil {
				return fmt.Errorf("failed to configure secret %s: %w", name, err)
			}
			configuredSecrets = append(configuredSecrets, name)
		}
	}

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Migration summary:")
	fmt.Fprintf(out, "- Workflows selected: %d\n", len(selectedWorkflows))
	fmt.Fprintf(out, "- Files copied: %d\n", len(copyResult.FilesCopied))
	fmt.Fprintf(out, "- Secrets detected: %d\n", len(detectedSecrets))
	fmt.Fprintf(out, "- Secrets configured: %d\n", len(configuredSecrets))

	if len(copyResult.FilesCopied) > 0 {
		fmt.Fprintln(out, "- Copied files:")
		for _, copiedPath := range copyResult.FilesCopied {
			rel, relErr := filepath.Rel(workDir, copiedPath)
			if relErr != nil {
				rel = copiedPath
			}
			fmt.Fprintf(out, "  - %s\n", rel)
		}
	}

	if len(configuredSecrets) > 0 {
		fmt.Fprintln(out, "- Configured secrets:")
		for _, name := range configuredSecrets {
			fmt.Fprintf(out, "  - %s\n", name)
		}
	}

	if len(warnings) > 0 {
		fmt.Fprintln(out, "- Warnings:")
		for _, warning := range warnings {
			fmt.Fprintf(out, "  - %s\n", warning)
		}
	}

	return nil
}

func parseSecretAssignments(secretFlags []string) (map[string]string, error) {
	assignments := make(map[string]string, len(secretFlags))
	for _, raw := range secretFlags {
		parts := strings.SplitN(raw, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid --secret value %q, expected KEY=VALUE", raw)
		}

		name := strings.TrimSpace(parts[0])
		if name == "" {
			return nil, fmt.Errorf("invalid --secret value %q, secret name cannot be empty", raw)
		}

		assignments[name] = parts[1]
	}

	return assignments, nil
}

func detectSecretsFromWorkflows(workflows []*migrate.WorkflowFile) ([]string, error) {
	all := make([]string, 0)
	for _, workflow := range workflows {
		secrets, err := migrate.DetectSecretsFromFile(workflow.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to detect secrets in %s: %w", workflow.Path, err)
		}
		all = append(all, secrets...)
	}

	if len(all) == 0 {
		return nil, nil
	}

	seen := make(map[string]struct{}, len(all))
	for _, secret := range all {
		if secret != "" {
			seen[secret] = struct{}{}
		}
	}

	deduped := make([]string, 0, len(seen))
	for secret := range seen {
		deduped = append(deduped, secret)
	}
	sort.Strings(deduped)

	return deduped, nil
}

func resolveMigrationAuth(ctx context.Context, opts migrateOptions) (string, string, error) {
	orgID := opts.orgID
	if orgID == "" {
		orgID = config.GetCurrentOrganization()
	}
	if orgID == "" {
		return "", "", fmt.Errorf("missing organization ID; pass --org or run `depot org switch`")
	}

	token, err := helpers.ResolveOrgAuth(ctx, opts.token)
	if err != nil {
		return "", "", err
	}
	if token == "" {
		return "", "", fmt.Errorf("missing API token, please run `depot login` or pass --token")
	}

	return orgID, token, nil
}
