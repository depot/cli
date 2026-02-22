package ci

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

// validStatuses are the user-facing status names accepted by --status.
var validStatuses = []string{"queued", "running", "finished", "failed", "cancelled"}

// parseStatus converts a user-facing status string to the proto enum value.
func parseStatus(s string) (civ1.CIRunStatus, error) {
	switch strings.ToLower(s) {
	case "queued":
		return civ1.CIRunStatus_CI_RUN_STATUS_QUEUED, nil
	case "running":
		return civ1.CIRunStatus_CI_RUN_STATUS_RUNNING, nil
	case "finished":
		return civ1.CIRunStatus_CI_RUN_STATUS_FINISHED, nil
	case "failed":
		return civ1.CIRunStatus_CI_RUN_STATUS_FAILED, nil
	case "cancelled":
		return civ1.CIRunStatus_CI_RUN_STATUS_CANCELLED, nil
	default:
		return 0, fmt.Errorf("invalid status %q, valid values: %s", s, strings.Join(validStatuses, ", "))
	}
}

// formatStatus converts a proto enum value to a short display string.
func formatStatus(s civ1.CIRunStatus) string {
	switch s {
	case civ1.CIRunStatus_CI_RUN_STATUS_QUEUED:
		return "queued"
	case civ1.CIRunStatus_CI_RUN_STATUS_RUNNING:
		return "running"
	case civ1.CIRunStatus_CI_RUN_STATUS_FINISHED:
		return "finished"
	case civ1.CIRunStatus_CI_RUN_STATUS_FAILED:
		return "failed"
	case civ1.CIRunStatus_CI_RUN_STATUS_CANCELLED:
		return "cancelled"
	default:
		return "unknown"
	}
}

func NewCmdRunList() *cobra.Command {
	var (
		token    string
		statuses []string
		n        int32
		output   string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List CI runs",
		Long:  `List CI runs for your organization.`,
		Example: `  # List runs (defaults to queued and running)
  depot ci run list

  # List failed runs
  depot ci run list --status failed

  # List finished and failed runs
  depot ci run list --status finished --status failed

  # List the 5 most recent runs
  depot ci run list -n 5

  # Output as JSON
  depot ci run list --output json`,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			var protoStatuses []civ1.CIRunStatus
			for _, s := range statuses {
				ps, err := parseStatus(s)
				if err != nil {
					return err
				}
				protoStatuses = append(protoStatuses, ps)
			}

			runs, err := api.CIListRuns(ctx, tokenVal, protoStatuses, n)
			if err != nil {
				return fmt.Errorf("failed to list runs: %w", err)
			}

			if output == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(runs)
			}

			if len(runs) == 0 {
				fmt.Println("No runs found.")
				return nil
			}

			fmt.Printf("%-24s %-30s %-12s %-10s %-12s %s\n", "RUN ID", "REPO", "SHA", "STATUS", "TRIGGER", "CREATED")
			fmt.Printf("%-24s %-30s %-12s %-10s %-12s %s\n",
				strings.Repeat("-", 24),
				strings.Repeat("-", 30),
				strings.Repeat("-", 12),
				strings.Repeat("-", 10),
				strings.Repeat("-", 12),
				strings.Repeat("-", 20),
			)

			for _, run := range runs {
				repo := run.Repo
				if len(repo) > 30 {
					repo = repo[:27] + "..."
				}

				sha := run.Sha
				if len(sha) > 12 {
					sha = sha[:12]
				}

				trigger := run.Trigger
				if len(trigger) > 12 {
					trigger = trigger[:9] + "..."
				}

				fmt.Printf("%-24s %-30s %-12s %-10s %-12s %s\n",
					run.RunId,
					repo,
					sha,
					formatStatus(run.Status),
					trigger,
					run.CreatedAt,
				)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringSliceVar(&statuses, "status", nil, "Filter by status (repeatable: queued, running, finished, failed, cancelled)")
	cmd.Flags().Int32VarP(&n, "n", "n", 0, "Number of runs to return")
	cmd.Flags().StringVar(&output, "output", "", "Output format (json)")

	return cmd
}
