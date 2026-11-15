package ci

import (
	"encoding/json"
	"fmt"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

// m1Flags follows the existing organization authentication and output contract.
type m1Flags struct{ org, token, output string }

func (f *m1Flags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.org, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&f.token, "token", "", "Depot API token")
	cmd.Flags().StringVarP(&f.output, "output", "o", "", "Output format (json)")
}
func (f *m1Flags) auth(cmd *cobra.Command) (string, string, error) {
	if err := validateTextOrJSONOutput(f.output); err != nil {
		return "", "", err
	}
	org := f.org
	if org == "" {
		org = config.GetCurrentOrganization()
	}
	token, err := helpers.ResolveOrgAuth(cmd.Context(), f.token)
	if err != nil {
		return "", "", err
	}
	if token == "" {
		return "", "", fmt.Errorf("missing API token, please run `depot login`")
	}
	return token, org, nil
}
func m1WriteJSON(cmd *cobra.Command, value any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
func NewCmdPause() *cobra.Command  { return newCmdControl(true) }
func NewCmdResume() *cobra.Command { return newCmdControl(false) }
func newCmdControl(pause bool) *cobra.Command {
	verb := "resume"
	short := "Resume a paused CI run"
	if pause {
		verb = "pause"
		short = "Pause a CI run"
	}
	var flags m1Flags
	cmd := &cobra.Command{
		Use: verb + " <run-id>", Short: short,
		Long:    "Pause or resume a CI run. Repeating the same request is state-idempotent.\n\nThe server reports whether a wake is pending; resuming does not claim that jobs have completed.",
		Example: fmt.Sprintf("  # %s\n  depot ci %s <run-id>\n\n  # Inspect the server state as JSON\n  depot ci %s <run-id> --output json", short, verb, verb),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token, org, err := flags.auth(cmd)
			if err != nil {
				return err
			}
			control := api.CIResumeRun
			if pause {
				control = api.CIPauseRun
			}
			resp, err := control(cmd.Context(), token, org, args[0])
			if err != nil {
				return err
			}
			if outputIsJSON(flags.output) {
				return m1WriteJSON(cmd, resp.Raw)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Run %s: paused=%t, wake pending=%t, revision=%s\n", resp.RunID, resp.Paused, resp.WakePending, resp.Revision)
			return err
		},
	}
	flags.add(cmd)
	return cmd
}
