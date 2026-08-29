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
	"time"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"connectrpc.com/connect"
	"github.com/charmbracelet/colorprofile"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/ci/compat"
	"github.com/depot/cli/pkg/ci/migrate"
	"github.com/depot/cli/pkg/ci/transform"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/oidc"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
	"github.com/spf13/cobra"
)

type migrationForge string

const (
	migrationForgeGitHub migrationForge = "github"
	migrationForgeOrigin migrationForge = "origin"
)

func (f *migrationForge) String() string {
	if f == nil || *f == "" {
		return string(migrationForgeGitHub)
	}
	return string(*f)
}

func (f *migrationForge) Set(value string) error {
	switch migrationForge(value) {
	case migrationForgeGitHub, migrationForgeOrigin:
		*f = migrationForge(value)
		return nil
	default:
		return fmt.Errorf("must be one of github, origin")
	}
}

func (f *migrationForge) Type() string {
	return "forge"
}

func effectiveMigrationForge(forge migrationForge) migrationForge {
	if forge == "" {
		return migrationForgeGitHub
	}
	return forge
}

type repositoryAnalysisClient interface {
	GetRepositoryAnalysis(context.Context, *connect.Request[civ2.GetRepositoryMigrationAnalysisRequest]) (*connect.Response[civ2.GetRepositoryMigrationAnalysisResponse], error)
}

type migrateOptions struct {
	token                       string
	orgID                       string
	forge                       migrationForge
	yes                         bool
	overwrite                   bool
	dir                         string
	stdout                      io.Writer
	includeSecrets              []string
	includeVars                 []string
	secretMigrationBranchPrefix string
	secretMigrationRegistrar    secretMigrationIntentRegistrar
	secretMigrationNow          time.Time
	repositoryAnalysisClient    repositoryAnalysisClient
}

func NewCmdMigrate() *cobra.Command {
	return newCmdMigrate(migrateOptions{})
}

func newCmdMigrate(opts migrateOptions) *cobra.Command {
	if opts.forge == "" {
		opts.forge = migrationForgeGitHub
	}

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate GitHub Actions workflows to Depot CI",
		Long:  "Optimistically migrates GitHub Actions workflows into .depot/workflows/ with inline corrections and comments.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrate(cmd.Context(), migrateCommandOptions(cmd, opts))
		},
	}

	pf := cmd.PersistentFlags()
	pf.StringVar(&opts.token, "token", "", "Depot API token")
	pf.StringVar(&opts.orgID, "org", "", "Depot organization ID")
	pf.Var(&opts.forge, "forge", "Source-code forge to migrate from (github or origin)")
	pf.BoolVarP(&opts.yes, "yes", "y", false, "Run in non-interactive mode")

	cmd.Flags().BoolVar(&opts.overwrite, "overwrite", false, "Overwrite existing .depot/ directory")

	cmd.AddCommand(newCmdPreflight(&opts))
	cmd.AddCommand(newCmdWorkflows(&opts))
	cmd.AddCommand(newCmdSecretsAndVars(&opts))

	return cmd
}

func migrateCommandOptions(cmd *cobra.Command, opts migrateOptions) migrateOptions {
	if strings.TrimSpace(opts.dir) == "" {
		opts.dir = "."
	}
	if opts.stdout == nil {
		opts.stdout = cmd.OutOrStdout()
	}
	return opts
}

func newCmdSecretsAndVars(parentOpts *migrateOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets-and-vars",
		Short: "Import GitHub Actions secrets and variables into Depot CI",
		Long:  "Creates a one-shot GitHub Actions workflow that reads secrets and variables from the source repo and imports them into Depot CI via the depot CLI.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return secretsAndVars(cmd.Context(), migrateCommandOptions(cmd, *parentOpts))
		},
	}

	cmd.Flags().StringSliceVar(&parentOpts.includeSecrets, "secrets", nil, "Secret name(s) to include (repeatable)")
	cmd.Flags().StringSliceVar(&parentOpts.includeVars, "vars", nil, "Variable name(s) to include (repeatable)")
	cmd.Flags().StringVar(
		&parentOpts.secretMigrationBranchPrefix,
		"branch-prefix",
		oidc.SecretMigrationBranchPrefix,
		"Prefix for the temporary migration branch (the intent ID is appended)",
	)

	return cmd
}

func secretsAndVars(ctx context.Context, opts migrateOptions) error {
	workDir := opts.dir
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}

	token, orgID, err := resolveAuth(ctx, opts)
	if err != nil {
		return err
	}
	remote, _, err := detectSecretMigrationRemote(ctx, workDir, effectiveMigrationForge(opts.forge))
	if err != nil {
		return err
	}
	return createSecretMigrationFromRepository(ctx, opts, token, orgID, remote)
}

func createSecretMigrationFromRepository(
	ctx context.Context,
	opts migrateOptions,
	token, orgID, remote string,
) error {
	workDir := opts.dir
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	out := opts.stdout
	if out == nil {
		out = os.Stdout
	}

	secretNames := opts.includeSecrets
	variableNames := opts.includeVars
	if len(secretNames) == 0 || len(variableNames) == 0 {
		workflowDirs := []string{
			filepath.Join(workDir, ".github", "workflows"),
			filepath.Join(workDir, ".depot", "workflows"),
		}
		workflows, warnings, err := parseExistingWorkflowDirsWithWarnings(workflowDirs)
		if err != nil {
			return fmt.Errorf("failed to inspect GitHub Actions workflows: %w", err)
		}
		for _, warning := range warnings {
			fmt.Fprintf(out, "Warning: %s\n", warning)
		}
		if len(secretNames) == 0 {
			secretNames, err = detectSecretsFromWorkflows(workflows)
			if err != nil {
				return fmt.Errorf("failed to detect secrets: %w", err)
			}
		}
		if len(variableNames) == 0 {
			variableNames, err = detectVariablesFromWorkflows(workflows)
			if err != nil {
				return fmt.Errorf("failed to detect variables: %w", err)
			}
		}
	}
	secretNames = omitSecretMigrationNames(secretNames, "GITHUB_TOKEN", "DEPOT_TOKEN")
	variableNames = omitSecretMigrationNames(variableNames, "DEPOT_TOKEN")
	if len(secretNames) == 0 && len(variableNames) == 0 {
		fmt.Fprintln(out, "No secrets or variables found to import.")
		return nil
	}

	result, err := createSecretMigration(ctx, createSecretMigrationOptions{
		dir:          workDir,
		remote:       remote,
		token:        token,
		orgID:        orgID,
		secrets:      secretNames,
		variables:    variableNames,
		branchPrefix: opts.secretMigrationBranchPrefix,
		now:          opts.secretMigrationNow,
		registrar:    opts.secretMigrationRegistrar,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\nWe've committed a workflow to migrate your secrets and variables on branch %s.\n", result.branchName)
	fmt.Fprintln(out, "All you need to do is push it within 5 minutes:")
	fmt.Fprintf(out, "  git push %s %s\n", shellQuote(remote), shellQuote(result.branchName))
	return nil
}

func omitSecretMigrationNames(names []string, omittedNames ...string) []string {
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		omit := false
		for _, omittedName := range omittedNames {
			if strings.EqualFold(strings.TrimSpace(name), omittedName) {
				omit = true
				break
			}
		}
		if !omit {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

func newCmdWorkflows(parentOpts *migrateOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflows",
		Short: "Migrate and transform GitHub Actions workflows to .depot/workflows/",
		Long:  "Copies .github/workflows/ into .depot/workflows/, applying Depot CI transformations and compatibility fixes.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return workflowsWithContext(cmd.Context(), migrateCommandOptions(cmd, *parentOpts))
		},
	}

	cmd.Flags().BoolVar(&parentOpts.overwrite, "overwrite", false, "Overwrite existing .depot/ directory")

	return cmd
}

func newCmdPreflight(parentOpts *migrateOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "preflight",
		Short: "Check authentication and repository access for migration",
		Long:  "Validates authentication, detects a repository for the selected forge, and checks the access required to migrate it. GitHub checks the Depot Code Access app; Origin checks that the repository is available to the Depot organization.",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := migrateCommandOptions(cmd, *parentOpts)
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
	if effectiveMigrationForge(opts.forge) == migrationForgeOrigin {
		return cursorPreflight(ctx, opts)
	}
	return githubPreflight(ctx, opts)
}

func githubPreflight(ctx context.Context, opts migrateOptions) (*preflightResult, error) {
	workDir := opts.dir
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}

	out := opts.stdout
	if out == nil {
		out = os.Stdout
	}
	out = colorprofile.NewWriter(out, os.Environ())

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
	if effectiveMigrationForge(opts.forge) == migrationForgeOrigin {
		return workflowsWithContext(ctx, opts)
	}

	result, err := preflight(ctx, opts)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}

	_ = result // auth info available for future use

	return workflowsWithContext(ctx, opts)
}

func workflows(opts migrateOptions) error {
	return workflowsWithContext(context.Background(), opts)
}

func workflowsWithContext(ctx context.Context, opts migrateOptions) error {
	workDir := opts.dir
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}

	out := opts.stdout
	if out == nil {
		out = os.Stdout
	}
	out = colorprofile.NewWriter(out, os.Environ())

	bold := lipgloss.NewStyle().Bold(true)
	migrationRemote := "origin"
	var originAnalysis *cursorOriginAnalysis

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
		dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

		// Split workflows into supported (has at least one supported trigger) and unsupported-only
		var supportedWorkflows, unsupportedWorkflows []*migrate.WorkflowFile
		for _, workflow := range workflows {
			if hasAnySupportedTrigger(workflow.Triggers) {
				supportedWorkflows = append(supportedWorkflows, workflow)
			} else {
				unsupportedWorkflows = append(unsupportedWorkflows, workflow)
			}
		}

		// Huh v2 subtracts the title from the inferred option height, so each
		// multi-select includes that row explicitly.
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
					Height(len(opts)+1).
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
					Height(len(opts)+1).
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

	if effectiveMigrationForge(opts.forge) == migrationForgeOrigin {
		analysis, err := analyzeCursorOriginWorkflows(ctx, opts, selectedWorkflows)
		if err != nil {
			return err
		}
		originAnalysis = analysis
		migrationRemote = analysis.remote
		fmt.Fprintf(out, "\nDetected repository: %s\n", bold.Render(analysis.repositoryURL))
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

	depotWorkflowsDir := filepath.Join(depotDir, "workflows")
	if err := os.MkdirAll(depotWorkflowsDir, 0755); err != nil {
		return fmt.Errorf("failed to create .depot/workflows: %w", err)
	}

	// Mirror non-YAML sibling files living alongside workflows (helper scripts,
	// configs) into .depot/workflows/ so references to them resolve there. This runs
	// before the transform so that, under a partial migration, the copied siblings can
	// join the rewrite allow-list below — otherwise a selected workflow's reference to
	// a sibling script would keep pointing at .github/ even though the script was moved.
	siblings, err := migrate.CopyWorkflowSiblings(workflowsDir, depotWorkflowsDir)
	if err != nil {
		return fmt.Errorf("failed to copy workflow sibling files: %w", err)
	}

	// During a partial migration migratedWorkflows gates which .github/workflows/
	// references get rewritten (only selected workflows). The copied siblings now live
	// under .depot/workflows/, so add their relative paths to the set; without this a
	// reference like ".github/workflows/scripts/build.sh" inside a migrated workflow
	// would be left pointing at .github/ and the copy would go unused. Full migrations
	// (nil set) already rewrite every reference, so there is nothing to add there.
	if migratedWorkflows != nil {
		for _, sibling := range siblings {
			rel, err := filepath.Rel(depotWorkflowsDir, sibling)
			if err != nil {
				return fmt.Errorf("failed to resolve relative path for %s: %w", sibling, err)
			}
			migratedWorkflows[filepath.ToSlash(rel)] = true
		}
	}

	// Rewrite .github/ references in copied action files.
	depotActionsDir := filepath.Join(depotDir, "actions")
	if info, err := os.Stat(depotActionsDir); err == nil && info.IsDir() {
		if _, err := transform.RewriteGitHubPathsInDir(depotActionsDir, migratedWorkflows); err != nil {
			return fmt.Errorf("failed to rewrite paths in action files: %w", err)
		}
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

	// Now that migratedWorkflows includes the copied siblings, rewrite any .github/
	// paths inside those sibling files the same way action files are rewritten.
	for _, sibling := range siblings {
		if _, err := transform.RewriteGitHubPathsInFile(sibling, migratedWorkflows); err != nil {
			return fmt.Errorf("failed to rewrite paths in %s: %w", sibling, err)
		}
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
	if originAnalysis != nil && originAnalysis.response != nil {
		renderCursorOriginDiagnostics(out, originAnalysis.response.Msg)
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

	defaultBranch := detectDefaultBranch(workDir, migrationRemote)

	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "%s\n\n", bold.Render("Next steps:"))

	if len(detectedSecrets) > 0 || len(detectedVariables) > 0 {
		secretsSource := "GitHub"
		secretMigrationCommand := "depot ci migrate secrets-and-vars"
		if effectiveMigrationForge(opts.forge) == migrationForgeOrigin {
			secretsSource = "the source repository"
			secretMigrationCommand += " --forge=origin"
		}
		fmt.Fprintf(out, "  1. Your workflows depend on %d secret(s) and %d variable(s) which need to be imported from %s:\n", len(detectedSecrets), len(detectedVariables), secretsSource)
		fmt.Fprintf(out, "     - Import them automatically with `%s`\n", secretMigrationCommand)
		fmt.Fprintln(out, "     - Or import them manually with `depot ci secrets add` and `depot ci vars add`")
		if defaultBranch != "" {
			fmt.Fprintf(out, "  2. Activate these workflows by pushing and merging them into %s\n", bold.Render(defaultBranch))
		} else {
			fmt.Fprintln(out, "  2. Activate these workflows by pushing and merging them into your default branch")
		}
	} else {
		if defaultBranch != "" {
			fmt.Fprintf(out, "  Activate these workflows by pushing and merging them into %s\n", bold.Render(defaultBranch))
		} else {
			fmt.Fprintln(out, "  Activate these workflows by pushing and merging them into your default branch")
		}
	}

	fmt.Fprintln(out, "")

	return nil
}

// detectDefaultBranch returns the default branch name (e.g. "main") or empty string.
func detectDefaultBranch(dir, remote string) string {
	if strings.TrimSpace(remote) == "" {
		remote = "origin"
	}
	// Try symbolic-ref first (works when the remote's HEAD is set)
	if out, err := exec.Command("git", "-C", dir, "symbolic-ref", "refs/remotes/"+remote+"/HEAD").Output(); err == nil {
		branch := strings.TrimSpace(string(out))
		branch = strings.TrimPrefix(branch, "refs/remotes/"+remote+"/")
		if branch != "" {
			return branch
		}
	}

	// Fall back to checking for common default branch names
	for _, name := range []string{"main", "master"} {
		if err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "refs/remotes/"+remote+"/"+name).Run(); err == nil {
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
