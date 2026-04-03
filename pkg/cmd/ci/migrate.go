package ci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/ci/compat"
	"github.com/depot/cli/pkg/ci/migrate"
	"github.com/depot/cli/pkg/ci/transform"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

type migrateOptions struct {
	token      string
	orgID      string
	yes        bool
	overwrite  bool
	dir        string
	stdout     io.Writer
	branchName string
}

func NewCmdMigrate() *cobra.Command {
	var opts migrateOptions

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate GitHub Actions workflows to Depot CI [beta]",
		Long:  "Optimistically migrates GitHub Actions workflows into .depot/workflows/ with inline corrections and comments.",
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			runOpts.dir = "."
			runOpts.stdout = os.Stdout
			return runMigrate(cmd.Context(), runOpts)
		},
	}

	pf := cmd.PersistentFlags()
	pf.StringVar(&opts.token, "token", "", "Depot API token")
	pf.StringVar(&opts.orgID, "org", "", "Depot organization ID")
	pf.BoolVarP(&opts.yes, "yes", "y", false, "Run in non-interactive mode")

	cmd.Flags().BoolVar(&opts.overwrite, "overwrite", false, "Overwrite existing .depot/ directory")

	cmd.AddCommand(newCmdPreflight(&opts))
	cmd.AddCommand(newCmdWorkflows(&opts))
	cmd.AddCommand(newCmdSecretsAndVars(&opts))

	return cmd
}

func newCmdSecretsAndVars(parentOpts *migrateOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets-and-vars",
		Short: "Import GitHub Actions secrets and variables into Depot CI",
		Long:  "Creates a one-shot GitHub Actions workflow that reads secrets and variables from the source repo and imports them into Depot CI via the depot CLI.",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := *parentOpts
			opts.dir = "."
			opts.stdout = os.Stdout
			return secretsAndVars(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&parentOpts.branchName, "branch", "", "Override the branch name used for the migration workflow")

	return cmd
}

func secretsAndVars(ctx context.Context, opts migrateOptions) error {
	workDir := opts.dir
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}

	out := opts.stdout
	if out == nil {
		out = os.Stdout
	}

	bold := lipgloss.NewStyle().Bold(true)

	token, orgID, err := resolveAuth(ctx, opts)
	if err != nil {
		return err
	}

	// Detect repo
	repo := detectRepoFromGitRemote(workDir)
	if repo == "" {
		return fmt.Errorf("could not detect GitHub repository from git remotes — is this a GitHub repo with a configured remote?")
	}

	client := api.NewMigrationClient()

	if !opts.yes {
		if !helpers.IsTerminal() {
			return fmt.Errorf("interactive mode requires a terminal; rerun with --yes")
		}

		fmt.Fprintln(out, "")
		fmt.Fprintf(out, "This will push a GitHub Actions workflow to %s on a temporary branch.\n", bold.Render(repo))
		fmt.Fprintln(out, "The workflow runs immediately, reads your existing secrets and variables,")
		fmt.Fprintln(out, "and imports them into Depot CI. The branch is safe to delete afterwards.")
		fmt.Fprintln(out, "")
		preview := true
		if err := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Preview the workflow before creating it?").
				Affirmative("Yes, show me").
				Negative("No, go ahead").
				Value(&preview),
		)).Run(); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				fmt.Fprintln(out, "Cancelled.")
				return nil
			}
			return fmt.Errorf("failed to confirm: %w", err)
		}

		if preview {
			dryResp, err := client.ImportSecretsAndVars(ctx, api.WithAuthenticationAndOrg(
				connect.NewRequest(&civ1.ImportSecretsAndVarsRequest{Repo: repo, DryRun: true, BranchName: opts.branchName}),
				token, orgID,
			))
			if err != nil {
				var connectErr *connect.Error
				if errors.As(err, &connectErr) {
					return fmt.Errorf("%s", connectErr.Message())
				}
				return fmt.Errorf("failed to preview: %w", err)
			}

			dryResult := dryResp.Msg.GetResult()
			switch r := dryResult.(type) {
			case *civ1.ImportSecretsAndVarsResponse_DryRunResult:
				fmt.Fprintln(out, "")
				fmt.Fprintf(out, "Branch: %s\n", bold.Render(r.DryRunResult.GetBranchName()))
				fmt.Fprintf(out, "File:   .github/workflows/%s\n\n", bold.Render(r.DryRunResult.GetWorkflowName()))
				fmt.Fprintln(out, r.DryRunResult.GetWorkflowContent())
			default:
				fmt.Fprintln(out, "No secrets or variables found to import.")
				return nil
			}

			confirm := false
			if err := huh.NewForm(huh.NewGroup(
				huh.NewConfirm().
					Title("Create this workflow?").
					Affirmative("Yes").
					Negative("No").
					Value(&confirm),
			)).Run(); err != nil {
				if errors.Is(err, huh.ErrUserAborted) {
					fmt.Fprintln(out, "Cancelled.")
					return nil
				}
				return fmt.Errorf("failed to confirm: %w", err)
			}

			if !confirm {
				fmt.Fprintln(out, "Cancelled.")
				return nil
			}
		}
	}

	resp, err := client.ImportSecretsAndVars(ctx, api.WithAuthenticationAndOrg(
		connect.NewRequest(&civ1.ImportSecretsAndVarsRequest{Repo: repo, BranchName: opts.branchName}),
		token, orgID,
	))
	if err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return fmt.Errorf("%s", connectErr.Message())
		}
		return fmt.Errorf("failed to import secrets and variables: %w", err)
	}

	result := resp.Msg.GetResult()
	switch r := result.(type) {
	case *civ1.ImportSecretsAndVarsResponse_RunResult:
		fmt.Fprintf(out, "\nMigration workflow created. View it at:\n  %s\n\n", r.RunResult.GetWorkflowUrl())
	default:
		fmt.Fprintln(out, "No secrets or variables found to import.")
	}

	return nil
}

func newCmdWorkflows(parentOpts *migrateOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflows",
		Short: "Migrate and transform GitHub Actions workflows to .depot/workflows/",
		Long:  "Copies .github/workflows/ into .depot/workflows/, applying Depot CI transformations and compatibility fixes.",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := *parentOpts
			opts.dir = "."
			opts.stdout = os.Stdout
			return workflows(opts)
		},
	}

	cmd.Flags().BoolVar(&parentOpts.overwrite, "overwrite", false, "Overwrite existing .depot/ directory")

	return cmd
}

func newCmdPreflight(parentOpts *migrateOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "preflight",
		Short: "Check that the Depot Code Access app is installed and configured",
		Long:  "Validates authentication, detects the repository from the git remote, and checks that the Depot Code Access GitHub App is installed with the correct permissions and repository access.",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := *parentOpts
			opts.dir = "."
			opts.stdout = os.Stdout
			_, err := preflight(cmd.Context(), opts)
			return err
		},
	}
}

// resolveAuth returns a token and orgID for MigrationService calls.
// Org tokens (prefixed "depot_org_") carry their org context already, so
// orgID is left empty. Any other token requires an explicit org ID.
func resolveAuth(ctx context.Context, opts migrateOptions) (token, orgID string, err error) {
	token, err = helpers.ResolveOrgAuth(ctx, opts.token)
	if err != nil {
		return "", "", fmt.Errorf("authentication failed: %w", err)
	}
	if token == "" {
		return "", "", fmt.Errorf("missing API token — run `depot login`, set DEPOT_TOKEN, or pass --token")
	}

	if strings.HasPrefix(token, "depot_org_") {
		return token, "", nil
	}

	orgID = opts.orgID
	if orgID == "" {
		orgID = config.GetCurrentOrganization()
	}
	if orgID == "" {
		return "", "", fmt.Errorf("missing organization ID — pass --org or run `depot org switch`")
	}

	return token, orgID, nil
}

// preflightResult is returned by preflight on success.
type preflightResult struct {
	token string
	orgID string
	repo  string
}

// preflight ensures auth, detects the repo, and checks that the
// Depot Code Access app is installed with the right permissions and access.
// Returns nil result (and nil error) when the check fails with a user-facing
// message that has already been printed.
func preflight(ctx context.Context, opts migrateOptions) (*preflightResult, error) {
	workDir := opts.dir
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}

	out := opts.stdout
	if out == nil {
		out = os.Stdout
	}

	bold := lipgloss.NewStyle().Bold(true)

	token, orgID, err := resolveAuth(ctx, opts)
	if err != nil {
		return nil, err
	}

	// Detect repo from git remote
	repo := detectRepoFromGitRemote(workDir)
	if repo == "" {
		return nil, fmt.Errorf("could not detect GitHub repository from git remotes — is this a GitHub repo with a configured remote?")
	}

	repoOwner := strings.SplitN(repo, "/", 2)[0]
	fmt.Fprintf(out, "\n")
	fmt.Fprintf(out, "Detected repository: %s\n", bold.Render(repo))

	// Check Depot Code Access installation
	client := api.NewMigrationClient()
	resp, err := client.GetInstallation(ctx, api.WithAuthenticationAndOrg(
		connect.NewRequest(&civ1.GetInstallationRequest{Repo: repo}),
		token, orgID,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to check installation status: %w", err)
	}

	installations := resp.Msg.GetInstallations()

	// Find the installation for this repo's owner
	var matched *civ1.Installation
	for _, inst := range installations {
		if strings.EqualFold(inst.GetOwner(), repoOwner) {
			matched = inst
			break
		}
	}

	if matched == nil {
		slug := orgID
		if slug == "" {
			slug = "_"
		}

		fmt.Fprintf(out, "The Depot Code Access app is not installed for %s.\n\n", bold.Render(repoOwner))
		fmt.Fprintf(out, "Install it at: https://depot.dev/orgs/%s/github-actions/installation/create?codeAccess=true\n", slug)

		return nil, nil
	}

	if !matched.GetRepoAccessible() {
		fmt.Fprintf(out, "The Depot Code Access app is installed for %s but does not have access to %s.\n\n", bold.Render(repoOwner), bold.Render(repo))
		fmt.Fprintf(out, "Grant access at: %s\n", matched.GetSettingsUrl())
		return nil, nil
	}

	if matched.GetRequiresNewPerms() {
		fmt.Fprintf(out, "The Depot Code Access app needs updated permissions for %s.\n\n", bold.Render(repoOwner))
		fmt.Fprintf(out, "Accept the permissions update at: %s\n", matched.GetSettingsUrl())
		return nil, nil
	}

	fmt.Fprintf(out, "Depot Code Access app is installed and configured for %s\n\n", bold.Render(repo))

	return &preflightResult{token: token, orgID: orgID, repo: repo}, nil
}

func runMigrate(ctx context.Context, opts migrateOptions) error {
	result, err := preflight(ctx, opts)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}

	_ = result // auth info available for future use

	return workflows(opts)
}

func workflows(opts migrateOptions) error {
	workDir := opts.dir
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}

	out := opts.stdout
	if out == nil {
		out = os.Stdout
	}

	bold := lipgloss.NewStyle().Bold(true)

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

	workflows, _, err := parseWorkflowDirWithWarnings(workflowsDir)
	if err != nil {
		return fmt.Errorf("failed to parse workflow files: %w", err)
	}
	if len(workflows) == 0 {
		return fmt.Errorf("no valid workflow files found in .github/workflows")
	}

	// Workflow selection
	selectedWorkflows := workflows
	if !opts.yes {
		if !helpers.IsTerminal() {
			return fmt.Errorf("interactive mode requires a terminal; rerun with --yes")
		}

		greenStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#30a46c"))
		dimStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#9B9B9B", Dark: "#5C5C5C"})

		// Split workflows into supported (has at least one supported trigger) and unsupported-only
		var supportedWorkflows, unsupportedWorkflows []*migrate.WorkflowFile
		for _, workflow := range workflows {
			if hasAnySupportedTrigger(workflow.Triggers) {
				supportedWorkflows = append(supportedWorkflows, workflow)
			} else {
				unsupportedWorkflows = append(unsupportedWorkflows, workflow)
			}
		}

		var groups []*huh.Group

		// Supported triggers group
		var selectedSupported []string
		if len(supportedWorkflows) > 0 {
			opts := make([]huh.Option[string], 0, len(supportedWorkflows))
			for _, wf := range supportedWorkflows {
				label := fmt.Sprintf("%s - %s", filepath.Base(wf.Path), colorizeTriggers(wf.Triggers, greenStyle, dimStyle))
				opts = append(opts, huh.NewOption(label, wf.Path))
			}
			selectedSupported = make([]string, 0, len(supportedWorkflows))
			for _, wf := range supportedWorkflows {
				selectedSupported = append(selectedSupported, wf.Path)
			}
			groups = append(groups, huh.NewGroup(
				huh.NewMultiSelect[string]().
					Title("These workflows have supported triggers. Which should we migrate?").
					Options(opts...).
					Value(&selectedSupported),
			))
		}

		// Unsupported-only triggers group
		var selectedUnsupported []string
		if len(unsupportedWorkflows) > 0 {
			opts := make([]huh.Option[string], 0, len(unsupportedWorkflows))
			for _, wf := range unsupportedWorkflows {
				label := fmt.Sprintf("%s - %s", filepath.Base(wf.Path), colorizeTriggers(wf.Triggers, greenStyle, dimStyle))
				opts = append(opts, huh.NewOption(label, wf.Path))
			}
			groups = append(groups, huh.NewGroup(
				huh.NewMultiSelect[string]().
					Title("These workflows have unsupported triggers. Migrate anyway?").
					Options(opts...).
					Value(&selectedUnsupported),
			))
		}

		form := huh.NewForm(groups...)

		if err := form.Run(); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				fmt.Fprintln(out, "Migration cancelled.")
				return nil
			}
			return fmt.Errorf("failed to select workflows: %w", err)
		}

		selected := append(selectedSupported, selectedUnsupported...)
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

	// Handle .depot/ overwrite
	copyMode := migrate.CopyModeError
	depotDir := filepath.Join(workDir, ".depot")
	if depotInfo, err := os.Stat(depotDir); err == nil {
		if !depotInfo.IsDir() {
			return fmt.Errorf(".depot exists but is not a directory")
		}
		if opts.overwrite || opts.yes {
			copyMode = migrate.CopyModeOverwrite
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

	// Copy .github/actions/ to .depot/actions/
	if _, err := migrate.CopyGitHubToDepot(workDir, []string{"actions"}, copyMode); err != nil {
		return fmt.Errorf("failed to copy GitHub CI files: %w", err)
	}

	// Build set of migrated workflow relative paths for selective rewriting.
	// When all workflows are selected, pass nil so all .github/workflows/ references
	// (including bare directory refs) are rewritten.
	var migratedWorkflows map[string]bool
	if len(selectedWorkflows) < len(workflows) {
		migratedWorkflows = make(map[string]bool, len(selectedWorkflows))
		for _, wf := range selectedWorkflows {
			relPath, err := filepath.Rel(workflowsDir, wf.Path)
			if err != nil {
				return fmt.Errorf("failed to resolve relative path for %s: %w", wf.Path, err)
			}
			migratedWorkflows[filepath.ToSlash(relPath)] = true
		}
	}

	// Rewrite .github/ references in copied action files
	depotActionsDir := filepath.Join(depotDir, "actions")
	if info, err := os.Stat(depotActionsDir); err == nil && info.IsDir() {
		if _, err := transform.RewriteGitHubPathsInDir(depotActionsDir, migratedWorkflows); err != nil {
			return fmt.Errorf("failed to rewrite paths in action files: %w", err)
		}
	}

	// Transform and write each workflow
	depotWorkflowsDir := filepath.Join(depotDir, "workflows")
	if err := os.MkdirAll(depotWorkflowsDir, 0755); err != nil {
		return fmt.Errorf("failed to create .depot/workflows: %w", err)
	}

	type workflowResult struct {
		filename    string
		result      *transform.TransformResult
		hasCritical bool
	}
	var results []workflowResult

	for _, wf := range selectedWorkflows {
		raw, err := os.ReadFile(wf.Path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", wf.Path, err)
		}

		report := compat.AnalyzeWorkflow(wf)
		result, err := transform.TransformWorkflow(raw, wf, report, migratedWorkflows)
		if err != nil {
			return fmt.Errorf("failed to transform %s: %w", filepath.Base(wf.Path), err)
		}

		relPath, err := filepath.Rel(workflowsDir, wf.Path)
		if err != nil {
			return fmt.Errorf("failed to resolve relative path for %s: %w", wf.Path, err)
		}

		destPath := filepath.Join(depotWorkflowsDir, relPath)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", destPath, err)
		}

		if err := os.WriteFile(destPath, result.Content, 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", destPath, err)
		}

		results = append(results, workflowResult{
			filename:    filepath.Base(wf.Path),
			result:      result,
			hasCritical: result.HasCritical,
		})
	}

	// Print summary
	skipped := len(workflows) - len(selectedWorkflows)
	if skipped > 0 {
		fmt.Fprintf(out, "%s %d workflow(s) to .depot/workflows/ (%d skipped)\n\n", bold.Render("Migrated"), len(results), skipped)
	} else {
		fmt.Fprintf(out, "%s %d workflow(s) to .depot/workflows/\n\n", bold.Render("Migrated"), len(results))
	}

	for _, r := range results {
		status := "migrated as is"
		if r.hasCritical {
			disabledCount := 0
			for _, c := range r.result.Changes {
				if c.Type == transform.ChangeJobDisabled {
					disabledCount++
				}
			}
			status = fmt.Sprintf("%d job(s) disabled (needs review)", disabledCount)
		} else if len(r.result.Changes) > 0 {
			status = fmt.Sprintf("%d change(s) applied", len(r.result.Changes))
		}
		fmt.Fprintf(out, "  %s — %s\n", r.filename, status)
	}

	// Detect secrets and variables
	detectedSecrets, err := detectSecretsFromWorkflows(selectedWorkflows)
	if err != nil {
		return fmt.Errorf("failed to detect secrets: %w", err)
	}

	detectedVariables, err := detectVariablesFromWorkflows(selectedWorkflows)
	if err != nil {
		return fmt.Errorf("failed to detect variables: %w", err)
	}

	defaultBranch := detectDefaultBranch(workDir)

	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "%s\n\n", bold.Render("Next steps:"))
	if defaultBranch != "" {
		fmt.Fprintf(out, "  1. Activate these workflows by pushing and merging them into %s\n", bold.Render(defaultBranch))
	} else {
		fmt.Fprintln(out, "  1. Activate these workflows by pushing and merging them into your default branch")
	}

	if len(detectedSecrets) > 0 || len(detectedVariables) > 0 {
		fmt.Fprintf(out, "  2. Your workflows depend on %d secret(s) and %d variable(s) which need to be imported from GitHub:\n", len(detectedSecrets), len(detectedVariables))
		fmt.Fprintln(out, "     - Import them automatically with `depot ci migrate secrets-and-vars`")
		fmt.Fprintln(out, "     - Or import them manually with `depot ci secrets add` and `depot ci vars add`")
	}

	fmt.Fprintln(out, "")

	return nil
}

// detectDefaultBranch returns the default branch name (e.g. "main") or empty string.
func detectDefaultBranch(dir string) string {
	// Try symbolic-ref first (works when origin/HEAD is set)
	if out, err := exec.Command("git", "-C", dir, "symbolic-ref", "refs/remotes/origin/HEAD").Output(); err == nil {
		branch := strings.TrimSpace(string(out))
		branch = strings.TrimPrefix(branch, "refs/remotes/origin/")
		if branch != "" {
			return branch
		}
	}

	// Fall back to checking for common default branch names
	for _, name := range []string{"main", "master"} {
		if err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "refs/remotes/origin/"+name).Run(); err == nil {
			return name
		}
	}

	return ""
}

// hasAnySupportedTrigger returns true if at least one trigger is not explicitly unsupported.
func hasAnySupportedTrigger(triggers []string) bool {
	for _, trigger := range triggers {
		rule, ok := compat.TriggerRules[trigger]
		if !ok || rule.Supported != compat.Unsupported {
			return true
		}
	}
	return len(triggers) == 0 // no triggers = treat as supported
}

// colorizeTriggers renders each trigger name in green (supported) or red (unsupported).
func colorizeTriggers(triggers []string, green, dim lipgloss.Style) string {
	parts := make([]string, len(triggers))
	for i, trigger := range triggers {
		rule, ok := compat.TriggerRules[trigger]
		if ok && rule.Supported == compat.Unsupported {
			parts[i] = dim.Render(trigger)
		} else {
			parts[i] = green.Render(trigger)
		}
	}
	return strings.Join(parts, ", ")
}
