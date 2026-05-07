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
	ciGetJobSummary = api.CIGetJobSummary
)

func NewCmdSummary() *cobra.Command {
	var (
		orgID  string
		token  string
		output string
	)

	cmd := &cobra.Command{
		Use:   "summary <attempt-id | job-id>",
		Short: "Fetch CI step summary markdown",
		Long:  "Fetch authored CI step summary markdown for one job attempt or the current/latest attempt of one job.",
		Example: `  depot ci summary <attempt-id>
  depot ci summary <job-id>
  depot ci summary <attempt-id> --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTextOrJSONOutput(output); err != nil {
				return err
			}
			if len(args) == 0 {
				if outputIsJSON(output) {
					cmd.SilenceUsage = true
					return fmt.Errorf("expected exactly one attempt or job ID")
				}
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

			resp, jobErr := ciGetJobSummary(ctx, tokenVal, orgID, &civ1.GetJobSummaryRequest{JobId: id})
			if jobErr == nil {
				if outputIsJSON(output) {
					return writeJSON(buildSummaryJSON(resp))
				}
				if resp.GetAttemptId() != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "Using attempt #%d %s for job %s.\n", resp.GetAttempt(), resp.GetAttemptId(), resp.GetJobId())
				}
				return printSummaryResponse(cmd.OutOrStdout(), resp)
			}
			if connect.CodeOf(jobErr) != connect.CodeNotFound {
				return fmt.Errorf("failed to get job summary: %w", jobErr)
			}

			resp, attemptErr := ciGetJobSummary(ctx, tokenVal, orgID, &civ1.GetJobSummaryRequest{AttemptId: id})
			if attemptErr != nil {
				if connect.CodeOf(attemptErr) == connect.CodeNotFound {
					return fmt.Errorf(
						"could not resolve %q as an attempt or job ID:\n  as attempt: %v\n  as job: %v",
						id,
						attemptErr,
						jobErr,
					)
				}
				return fmt.Errorf("failed to get attempt summary: %w", attemptErr)
			}

			if outputIsJSON(output) {
				return writeJSON(buildSummaryJSON(resp))
			}
			return printSummaryResponse(cmd.OutOrStdout(), resp)
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (text, json)")

	return cmd
}

type summaryJSONDocument struct {
	OrgID         string `json:"org_id"`
	RunID         string `json:"run_id"`
	WorkflowID    string `json:"workflow_id"`
	JobID         string `json:"job_id"`
	AttemptID     string `json:"attempt_id"`
	Attempt       int32  `json:"attempt"`
	JobStatus     string `json:"job_status"`
	AttemptStatus string `json:"attempt_status"`
	HasSummary    bool   `json:"has_summary"`
	EmptyReason   string `json:"empty_reason"`
	StepCount     uint32 `json:"step_count"`
	Markdown      string `json:"markdown"`
}

func buildSummaryJSON(resp *civ1.GetJobSummaryResponse) summaryJSONDocument {
	return summaryJSONDocument{
		OrgID:         resp.GetOrgId(),
		RunID:         resp.GetRunId(),
		WorkflowID:    resp.GetWorkflowId(),
		JobID:         resp.GetJobId(),
		AttemptID:     resp.GetAttemptId(),
		Attempt:       resp.GetAttempt(),
		JobStatus:     resp.GetJobStatus(),
		AttemptStatus: resp.GetAttemptStatus(),
		HasSummary:    resp.GetHasSummary(),
		EmptyReason:   resp.GetEmptyReason(),
		StepCount:     resp.GetStepCount(),
		Markdown:      resp.GetMarkdown(),
	}
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
