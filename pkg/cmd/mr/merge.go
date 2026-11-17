package mr

import (
	"fmt"
	"regexp"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

var mergeOID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func newMerge(o *options) *cobra.Command {
	var squash bool
	var t target
	var expectedHead, expectedBase, expectedBaseRef string
	cmd := &cobra.Command{Use: "merge [branch]", Short: "Merge an observed revision of a merge request",
		Long: "Merge an open merge request using the local branch commit as the expected head.\nA newer remote head is refused. Update your local branch and review it before retrying.\nPass --id with --expected-head-oid and --expected-base-ref to merge an exact MR without a local checkout.\nPass --expected-base-oid to also pin the destination commit; otherwise the server guards the destination tip it observes while merging.\nCI results and approvals do not block merging.\nBy default the branch lands as a merge commit. Pass --squash to land it as one commit, with the merge request title and description as its message; your branch is not changed.", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := t.check(cmd, args)
			if err != nil {
				return err
			}
			head := expectedHead
			if name == "" {
				if !cmd.Flags().Changed("expected-head-oid") || !cmd.Flags().Changed("expected-base-ref") {
					return fmt.Errorf("--id requires --expected-head-oid and --expected-base-ref; use the head and destination you reviewed")
				}
				if !mergeOID.MatchString(head) {
					return fmt.Errorf("--expected-head-oid must be a lowercase hexadecimal SHA-1 or SHA-256 object ID")
				}
			} else {
				if cmd.Flags().Changed("expected-head-oid") {
					return fmt.Errorf("--expected-head-oid requires --id; branch mode always uses the local commit")
				}
				head, err = git(cmd, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+name+"^{commit}")
				if err != nil {
					return fmt.Errorf("cannot resolve local branch %q; check it out before merging", name)
				}
			}
			var base, baseRef *string
			if cmd.Flags().Changed("expected-base-oid") {
				if !mergeOID.MatchString(expectedBase) {
					return fmt.Errorf("--expected-base-oid must be a lowercase hexadecimal SHA-1 or SHA-256 object ID")
				}
				base = &expectedBase
			}
			if cmd.Flags().Changed("expected-base-ref") {
				if _, err := branch(cmd, []string{expectedBaseRef}); err != nil {
					return fmt.Errorf("--expected-base-ref: %w", err)
				}
				baseRef = &expectedBaseRef
			}
			if err := o.authenticateFor(cmd, name); err != nil {
				return err
			}
			// ID lookup does not scope by repository; enforce an explicit --repo before the mutation.
			if t.id != "" && o.repo != "" {
				found, err := o.fetch(cmd, &code.GetMergeRequestRequest{Lookup: &code.GetMergeRequestRequest_MergeRequestId{MergeRequestId: t.id}})
				if err != nil {
					return err
				}
				if found.MergeRequest.Id != t.id || found.MergeRequest.RepositoryId != o.repo {
					return fmt.Errorf("merge request %s does not belong to repository %s", t.id, o.repo)
				}
			}
			lookup := &code.GetMergeRequestRequest_HeadRef{RepositoryId: o.repo, HeadRef: name}
			client := api.NewMergeRequestClient()
			out := &code.MergeMergeRequestResponse{}
			if squash || t.id != "" || baseRef != nil {
				req := &code.MergeMergeRequestWithMethodRequest{
					Lookup:          &code.MergeMergeRequestWithMethodRequest_Head{Head: lookup},
					ExpectedHeadOid: &head, ExpectedBaseOid: base, ExpectedBaseRef: baseRef,
					Method: code.MergeMergeRequestWithMethodRequest_METHOD_MERGE_COMMIT,
				}
				if t.id != "" {
					req.Lookup = &code.MergeMergeRequestWithMethodRequest_MergeRequestId{MergeRequestId: t.id}
				}
				if squash {
					req.Method = code.MergeMergeRequestWithMethodRequest_METHOD_SQUASH
				}
				resp, err := client.MergeMergeRequestWithMethod(cmd.Context(), request(o, req))
				if connect.CodeOf(err) == connect.CodeUnimplemented {
					if squash {
						return fmt.Errorf("this server does not support --squash; nothing was merged: %w", err)
					}
					return fmt.Errorf("this server does not support the requested merge guards; nothing was merged: %w", err)
				}
				if err != nil {
					return err
				}
				out.MergeRequest = resp.Msg.MergeRequest
			} else {
				resp, err := client.MergeMergeRequest(cmd.Context(), request(o, &code.MergeMergeRequestRequest{
					Lookup: &code.MergeMergeRequestRequest_Head{Head: lookup}, ExpectedHeadOid: &head, ExpectedBaseOid: base,
				}))
				if err != nil {
					return err
				}
				out = resp.Msg
			}
			mr := out.MergeRequest
			if mr == nil || mr.Id == "" || mr.State != code.MergeRequest_STATE_MERGED || mr.MergeCommitOid == "" {
				return fmt.Errorf("API returned no completed merge result")
			}
			if baseRef != nil && mr.BaseRef != *baseRef {
				return fmt.Errorf("API returned a merge into %q instead of %q; inspect the merge request before retrying", mr.BaseRef, *baseRef)
			}
			if t.id != "" && (mr.Id != t.id || mr.HeadOid != head || (o.repo != "" && mr.RepositoryId != o.repo)) {
				return fmt.Errorf("API returned a different merge result; inspect merge request %s before retrying", t.id)
			}
			if o.json {
				return o.writeJSON(cmd, out)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Merged %s: %s\n", mr.Id, mr.MergeCommitOid)
			return err
		},
	}
	t.register(cmd)
	cmd.Flags().StringVar(&expectedHead, "expected-head-oid", "", "Reviewed source commit (required with --id)")
	cmd.Flags().StringVar(&expectedBase, "expected-base-oid", "", "Expected destination commit; a moved destination is refused")
	cmd.Flags().StringVar(&expectedBaseRef, "expected-base-ref", "", "Expected destination branch (required with --id); retargeting is refused")
	cmd.Flags().BoolVar(&squash, "squash", false, "Land the branch as one commit instead of a merge commit")
	return cmd
}
