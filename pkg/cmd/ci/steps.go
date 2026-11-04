package ci

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

// NewCmdSteps returns recorded execution timings for one specific CI attempt.
func NewCmdSteps() *cobra.Command {
	var orgID, token, output string
	cmd := &cobra.Command{
		Use:     "steps <attempt-id>",
		Short:   "Fetch CI step status and timing",
		Long:    "Fetch recorded execution steps for one CI job attempt. Missing timings remain absent; log timestamps are not used to estimate duration.",
		Args:    cobra.ExactArgs(1),
		Example: "  depot ci steps att_123\n  depot ci steps att_123 --output json",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTextOrJSONOutput(output); err != nil {
				return err
			}
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("attempt ID is required")
			}
			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}
			tokenVal, err := helpers.ResolveOrgAuth(cmd.Context(), token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}
			resp, err := api.CIGetJobAttemptSteps(cmd.Context(), tokenVal, orgID, args[0])
			if err != nil {
				return fmt.Errorf("failed to get CI steps: %w", err)
			}
			if outputIsJSON(output) {
				data, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true, Indent: "  "}).Marshal(resp)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return err
			}
			if !resp.Available {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "No step timings were recorded for this attempt. Missing data does not mean zero duration.")
				return err
			}
			if !resp.Complete {
				fmt.Fprintln(cmd.OutOrStdout(), "Progress snapshot: final step results have not been recorded.")
			}
			if resp.Truncated {
				fmt.Fprintln(cmd.OutOrStdout(), "Partial result: recorded step metadata exceeded the capture limit.")
			}
			if len(resp.Steps) == 0 {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "The worker reported no execution steps.")
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "STEP\tID\tKEY\tNAME\tSTATUS\tRESULT\tDURATION\tSTARTED\tCOMPLETED")
			for _, step := range resp.Steps {
				duration := "-"
				if step.DurationMs != nil {
					duration = fmt.Sprintf("%d.%03ds", step.GetDurationMs()/1000, step.GetDurationMs()%1000)
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", step.Number, stepDisplayText(step.StepId), stepDisplayText(step.StepKey), stepDisplayText(step.Name), stepDisplayText(step.Status), stepDisplayText(step.Conclusion), duration, stepDisplayText(step.GetStartedAt()), stepDisplayText(step.GetCompletedAt()))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format (text, json)")
	return cmd
}

func stepDisplayText(value string) string {
	if value == "" {
		return "-"
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}
