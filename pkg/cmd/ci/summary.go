package ci

import (
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

var (
	ciGetJobAttemptSummary = api.CIGetJobAttemptSummary
	ciGetJobSummary        = api.CIGetJobSummary
)

func NewCmdSummary() *cobra.Command {
	var (
		orgID string
		token string
	)

	cmd := &cobra.Command{
		Use:   "summary <attempt-id | job-id>",
		Short: "Fetch CI step summary markdown",
		Long:  "Fetch authored CI step summary markdown for one job attempt or the current/latest attempt of one job.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			if len(args) > 1 {
				return fmt.Errorf("expected exactly one attempt or job ID")
			}

			ctx := cmd.Context()
			id := args[0]

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

			resp, attemptErr := ciGetJobAttemptSummary(ctx, tokenVal, orgID, id)
			if attemptErr == nil {
				return printSummaryResponse(cmd.OutOrStdout(), resp)
			}
			if connect.CodeOf(attemptErr) != connect.CodeNotFound {
				return fmt.Errorf("failed to get attempt summary: %w", attemptErr)
			}

			resp, jobErr := ciGetJobSummary(ctx, tokenVal, orgID, id)
			if jobErr != nil {
				if connect.CodeOf(jobErr) == connect.CodeNotFound {
					return fmt.Errorf(
						"could not resolve %q as an attempt or job ID:\n  as attempt: %v\n  as job: %v",
						id,
						attemptErr,
						jobErr,
					)
				}
				return fmt.Errorf("failed to get job summary: %w", jobErr)
			}

			if resp.GetAttemptId() != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Using attempt #%d %s for job %s.\n", resp.GetAttempt(), resp.GetAttemptId(), resp.GetJobId())
			}
			return printSummaryResponse(cmd.OutOrStdout(), resp)
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")

	return cmd
}

func printSummaryResponse(w io.Writer, resp *civ1.GetJobSummaryResponse) error {
	if resp.GetHasSummary() {
		fmt.Fprint(w, resp.GetMarkdown())
		if !strings.HasSuffix(resp.GetMarkdown(), "\n") {
			fmt.Fprintln(w)
		}
		return nil
	}

	fmt.Fprintln(w, emptySummaryMessage(resp))
	return nil
}

func emptySummaryMessage(resp *civ1.GetJobSummaryResponse) string {
	switch resp.GetEmptyReason() {
	case "no_attempt":
		return "No CI job attempts exist yet, so no step summary is available."
	default:
		if resp.GetAttemptId() != "" {
			return "No CI step summary was produced for this attempt."
		}
		return "No CI step summary was produced."
	}
}
