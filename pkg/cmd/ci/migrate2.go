package ci

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/depot/cli/pkg/ci/compat"
	"github.com/depot/cli/pkg/ci/migrate"
	"github.com/depot/cli/pkg/ci/transform"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

type migrate2Options struct {
	yes       bool
	overwrite bool
	dir       string
	stdout    io.Writer
}

func NewCmdMigrate2() *cobra.Command {
	var opts migrate2Options

	cmd := &cobra.Command{
		Use:   "migrate2",
		Short: "Migrate GitHub Actions workflows to Depot CI [beta]",
		Long:  "Optimistically migrates GitHub Actions workflows into .depot/workflows/ with inline corrections and comments.",
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			runOpts.dir = "."
			runOpts.stdout = os.Stdout
			return runMigrate2(runOpts)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&opts.yes, "yes", false, "Run in non-interactive mode")
	flags.BoolVar(&opts.overwrite, "overwrite", false, "Overwrite existing .depot/ directory")

	return cmd
}

func runMigrate2(opts migrate2Options) error {
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

	workflows, _, err := parseWorkflowDirWithWarnings(workflowsDir)
	if err != nil {
		return fmt.Errorf("failed to parse workflow files: %w", err)
	}
	if len(workflows) == 0 {
		return fmt.Errorf("no valid workflow files found in .github/workflows")
	}

	fmt.Fprintf(out, "Found %d workflow(s) in .github/workflows\n\n", len(workflows))

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
		result, err := transform.TransformWorkflow(raw, wf, report)
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

	// Detect secrets and variables
	detectedSecrets, err := detectSecretsFromWorkflows(selectedWorkflows)
	if err != nil {
		return fmt.Errorf("failed to detect secrets: %w", err)
	}

	detectedVariables, err := detectVariablesFromWorkflows(selectedWorkflows)
	if err != nil {
		return fmt.Errorf("failed to detect variables: %w", err)
	}

	// Print summary
	fmt.Fprintf(out, "Migrated %d workflow(s) to .depot/workflows/\n\n", len(results))

	criticalCount := 0
	for _, r := range results {
		status := "no issues"
		if r.hasCritical {
			criticalCount++
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

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Next steps:")
	fmt.Fprintln(out, "  1. Review the migrated workflows in .depot/workflows/")
	fmt.Fprintln(out, "  2. Commit and merge into your default branch")
	fmt.Fprintln(out, "  3. Install the Depot Code Access app: https://github.com/apps/depot-code-access")

	if len(detectedSecrets) > 0 || len(detectedVariables) > 0 {
		settingsURL := "your Depot CI settings"
		if orgID := config.GetCurrentOrganization(); orgID != "" {
			settingsURL = fmt.Sprintf("https://depot.dev/orgs/%s/ci/settings", orgID)
		}
		fmt.Fprintf(out, "  4. %d secret(s) and %d variable(s) detected — import them at %s\n", len(detectedSecrets), len(detectedVariables), settingsURL)
	}

	return nil
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
