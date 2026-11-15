package ci

import (
	"fmt"
	"strings"

	"github.com/depot/cli/pkg/api"
	"github.com/spf13/cobra"
)

func NewCmdInput() *cobra.Command {
	var flags m1Flags
	var attempt string
	var fields []string
	cmd := &cobra.Command{
		Use: "input <job-id>", Short: "Inspect or answer a CI job input request",
		Long: `Without fields, inspect the server's public question, policy, and approval progress as JSON.

To answer, supply the current attempt ID and one or more fields. The server authenticates the responder and enforces the policy. A recorded approval may still be waiting for quorum; it is not job completion.`,
		Example: `  # Inspect the current input request
  depot ci input <job-id>

  # Answer an approval request for this attempt
  depot ci input <job-id> --attempt <attempt-id> --field decision=approve

  # Inspect the server response
  depot ci input <job-id> --attempt <attempt-id> --field decision=reject --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			values, err := parseJobInputFields(fields)
			if err != nil {
				return err
			}
			if len(values) > 0 && strings.TrimSpace(attempt) == "" {
				return fmt.Errorf("--attempt is required when answering an input request")
			}
			token, org, err := flags.auth(cmd)
			if err != nil {
				return err
			}
			if len(values) == 0 {
				job, err := api.CIGetJobPublicJSON(cmd.Context(), token, org, args[0])
				if err != nil {
					return err
				}
				return m1WriteJSON(cmd, job)
			}
			resp, err := api.CISubmitJobInput(cmd.Context(), token, org, args[0], attempt, values)
			if err != nil {
				return err
			}
			if outputIsJSON(flags.output) {
				return m1WriteJSON(cmd, resp.Raw)
			}
			if resp.Pending {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Approval recorded; waiting for the approval policy to be satisfied (%d approvals recorded)\n", resp.ApprovalCount)
				return err
			}
			// Legacy {} responses finish ordinary input submission, not necessarily the
			// whole job. Always read authoritative status, including rejected gates.
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Input recorded; current job status:"); err != nil {
				return err
			}
			job, err := api.CIGetJobPublicJSON(cmd.Context(), token, org, args[0])
			if err != nil {
				return err
			}
			return m1WriteJSON(cmd, job)
		},
	}
	flags.add(cmd)
	cmd.Flags().StringVar(&attempt, "attempt", "", "Attempt ID (required when answering)")
	cmd.Flags().StringArrayVar(&fields, "field", nil, "Input as key=value (repeatable; decision=approve or decision=reject for approvals)")
	return cmd
}

func parseJobInputFields(raw []string) (map[string]string, error) {
	// Reuse dispatch's parsing and duplicate checks, without echoing values in
	// parse errors (an answer can be sensitive). All server-defined keys are legal.
	for _, entry := range raw {
		if strings.Index(entry, "=") <= 0 {
			return nil, fmt.Errorf("invalid --field: expected key=value")
		}
	}
	result, err := parseDispatchInputs(raw)
	if err != nil {
		return nil, fmt.Errorf("duplicate --field key")
	}
	return result, nil
}
