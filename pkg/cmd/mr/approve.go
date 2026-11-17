package mr

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

func newApprove(o *options) *cobra.Command {
	var t target
	var expectedHead string
	var withdraw bool
	cmd := &cobra.Command{Use: "approve [branch]", Short: "Approve a merge request or withdraw your approval",
		Long: "Approve a merge request at its current head. Pass --expected-head-oid to approve only the commit you reviewed; a newer head is refused.\nPass --id with --expected-head-oid to approve without a checkout. Use --withdraw to remove your own approval.", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := t.check(cmd, args)
			if err != nil {
				return err
			}
			guarded := cmd.Flags().Changed("expected-head-oid")
			if withdraw && guarded {
				return fmt.Errorf("--withdraw cannot be combined with --expected-head-oid")
			}
			if !withdraw && t.id != "" && !guarded {
				return fmt.Errorf("--id requires --expected-head-oid; use the head you reviewed")
			}
			if guarded && !mergeOID.MatchString(expectedHead) {
				return fmt.Errorf("--expected-head-oid must be a lowercase hexadecimal SHA-1 or SHA-256 object ID")
			}
			if err := o.authenticateFor(cmd, name); err != nil {
				return err
			}
			var found *code.GetMergeRequestResponse
			if t.id != "" {
				found, err = o.fetch(cmd, &code.GetMergeRequestRequest{Lookup: &code.GetMergeRequestRequest_MergeRequestId{MergeRequestId: t.id}})
			} else {
				found, err = o.get(cmd, name)
			}
			if err != nil {
				return err
			}
			mr := found.MergeRequest
			if (t.id != "" && mr.Id != t.id) || (o.repo != "" && mr.RepositoryId != o.repo) {
				return fmt.Errorf("API returned a different merge request or repository")
			}
			client := api.NewMergeRequestClient()
			if withdraw {
				resp, err := client.RemoveMergeRequestApproval(cmd.Context(), request(o, &code.RemoveMergeRequestApprovalRequest{MergeRequestId: mr.Id}))
				if err != nil {
					return err
				}
				if o.json {
					return o.writeJSON(cmd, resp.Msg)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Withdrew approval of %s\n", printable(mr.Id))
				return err
			}
			if err := requireState(mr, name, code.MergeRequest_STATE_OPEN); err != nil {
				return err
			}
			var out *code.ApproveMergeRequestResponse
			if guarded {
				resp, err := client.ApproveMergeRequestAtRevision(cmd.Context(), request(o, &code.ApproveMergeRequestAtRevisionRequest{MergeRequestId: mr.Id, ExpectedHeadOid: expectedHead}))
				if connect.CodeOf(err) == connect.CodeUnimplemented {
					return fmt.Errorf("this server does not support guarded approvals; nothing was approved: %w", err)
				}
				if err != nil {
					return err
				}
				out = &code.ApproveMergeRequestResponse{Approval: resp.Msg.Approval}
			} else {
				resp, err := client.ApproveMergeRequest(cmd.Context(), request(o, &code.ApproveMergeRequestRequest{MergeRequestId: mr.Id}))
				if err != nil {
					return err
				}
				out = resp.Msg
			}
			approval := out.Approval
			if approval == nil || approval.MergeRequestId != mr.Id || approval.HeadOid == "" || approval.WithdrawnAt != nil || (guarded && approval.HeadOid != expectedHead) {
				return fmt.Errorf("API did not confirm the requested approval; inspect the merge request before retrying")
			}
			if o.json {
				return o.writeJSON(cmd, out)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Approved %s at %s\n", printable(approval.MergeRequestId), printable(approval.HeadOid))
			return err
		},
	}
	t.register(cmd)
	cmd.Flags().StringVar(&expectedHead, "expected-head-oid", "", "Reviewed source commit (required with --id); a moved head is refused")
	cmd.Flags().BoolVar(&withdraw, "withdraw", false, "Withdraw your own approval")
	return cmd
}
