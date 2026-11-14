package mr

import (
	"fmt"
	"strings"

	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

func newClose(o *options) *cobra.Command {
	return newSetState(o, code.MergeRequest_STATE_CLOSED, "Closed", &cobra.Command{
		Use: "close [branch]", Short: "Close a merge request without merging it",
		Long: "Close an open merge request without merging it.\nThe branch defaults to the current branch. The output includes the merge request ID, which `depot mr reopen --id` needs.",
	})
}

func newReopen(o *options) *cobra.Command {
	return newSetState(o, code.MergeRequest_STATE_OPEN, "Reopened", &cobra.Command{
		Use: "reopen --id <merge request ID>", Short: "Reopen a closed merge request",
		Long: "Reopen a closed merge request while its source branch still exists.\nA closed merge request is found only by its ID, which `depot mr close` prints.",
	})
}

// newSetState finishes a command that moves a merge request to state and reports it with verb.
func newSetState(o *options, state code.MergeRequest_State, verb string, cmd *cobra.Command) *cobra.Command {
	var t target
	cmd.Args = cobra.MaximumNArgs(1)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		name, err := t.check(cmd, args)
		if err != nil {
			return err
		}
		if err := o.authenticateFor(cmd, name); err != nil {
			return err
		}
		id, err := t.resolve(cmd, o, name)
		if err != nil {
			return err
		}
		resp, err := o.update(cmd, &code.UpdateMergeRequestRequest{MergeRequestId: id, State: state})
		if err != nil {
			return err
		}
		if resp.MergeRequest.State != state {
			return fmt.Errorf("API left merge request %s %s", resp.MergeRequest.Id, stateName(resp.MergeRequest.State))
		}
		if o.json {
			return o.writeJSON(cmd, resp)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", verb, resp.MergeRequest.Id, resp.MergeRequest.Title)
		return err
	}
	t.register(cmd)
	return cmd
}

func stateName(state code.MergeRequest_State) string {
	return strings.ToLower(strings.TrimPrefix(state.String(), "STATE_"))
}
