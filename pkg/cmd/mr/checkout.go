package mr

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/depot/cli/pkg/coderepo"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

func newCheckout(o *options) *cobra.Command {
	var t target
	var localBranch string
	var detach, force bool
	cmd := &cobra.Command{Use: "checkout [branch]", Short: "Check out a merge request's recorded head commit", Args: cobra.MaximumNArgs(1),
		Long: "Check out an MR by branch or --id using Git and your configured Git credentials.\nLocal changes are never discarded. Existing local commits are preserved unless --force is passed.\nClosed, merged and deleted-head MRs require --detach; the recorded commit must still be fetchable.\nNo Git remotes or credential configuration are changed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := t.check(cmd, args)
			if err != nil {
				return err
			}
			if detach && (cmd.Flags().Changed("branch") || force) {
				return fmt.Errorf("--detach cannot be combined with --branch or --force")
			}
			if cmd.Flags().Changed("branch") {
				if _, err := branch(cmd, []string{localBranch}); err != nil {
					return err
				}
			}
			if err := o.authenticateUser(cmd); err != nil {
				return err
			}
			// Even explicit MR/repository IDs must belong to the local checkout.
			localRepo, err := coderepo.Infer(cmd, o.org, o.token, "")
			if err != nil {
				return err
			}
			if o.repo != "" && localRepo != o.repo {
				return fmt.Errorf("local checkout does not match --repo")
			}
			o.repo = localRepo
			var found *code.GetMergeRequestResponse
			if t.id != "" {
				found, err = o.getByID(cmd, t.id)
			} else {
				found, err = o.get(cmd, name)
			}
			if err != nil {
				return err
			}
			mr := found.MergeRequest
			if mr.RepositoryId != localRepo || (t.id != "" && mr.Id != t.id) {
				return fmt.Errorf("merge request does not match the selected repository or ID")
			}
			if !detach && (mr.State != code.MergeRequest_STATE_OPEN || mr.HeadBranchDeleted) {
				return fmt.Errorf("merge request is closed, merged or its head branch was deleted; use --detach to inspect its recorded commit")
			}
			if _, err := parseOID(mr.HeadOid); err != nil {
				return err
			}
			if localBranch == "" {
				localBranch = mr.HeadRef
			}
			if !detach {
				if _, err := branch(cmd, []string{localBranch}); err != nil {
					return err
				}
			}
			repo, err := coderepo.Lookup(cmd.Context(), o.org, o.token, mr.RepositoryId, "")
			if err != nil {
				return err
			}
			if !detach && localBranch == repo.DefaultBranch {
				return fmt.Errorf("refusing to reset the repository default branch; choose --branch or --detach")
			}
			if err := cleanCheckout(cmd); err != nil {
				return err
			}
			oid, err := git(cmd, "rev-parse", "--verify", mr.HeadOid+"^{commit}")
			if err != nil {
				// Fetch the recorded object, never a branch that may have been reused since the MR closed.
				if _, err := git(cmd, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--", repo.CloneURL, mr.HeadOid); err != nil {
					return fmt.Errorf("cannot fetch the MR's recorded head commit; check Git credentials and whether that commit is still available")
				}
				oid, err = git(cmd, "rev-parse", "--verify", mr.HeadOid+"^{commit}")
			}
			if err != nil || !strings.EqualFold(oid, mr.HeadOid) {
				return fmt.Errorf("local object does not match the MR head commit")
			}
			if !detach && !force {
				if _, err := git(cmd, "show-ref", "--verify", "--quiet", "refs/heads/"+localBranch); err == nil {
					if _, err := git(cmd, "merge-base", "--is-ancestor", "refs/heads/"+localBranch, oid); err != nil {
						return fmt.Errorf("local branch has commits outside this MR; choose --branch, --detach, or --force to reset it")
					}
				}
			}
			if err := cleanCheckout(cmd); err != nil {
				return err
			}
			checkoutArgs := []string{"checkout", "--quiet", "--no-overwrite-ignore", "--detach", oid}
			if !detach {
				checkoutArgs = []string{"checkout", "--quiet", "--no-overwrite-ignore", "-B", localBranch, oid}
			}
			if _, err := git(cmd, checkoutArgs...); err != nil {
				return fmt.Errorf("Git refused checkout; check for local changes or a branch in use by another worktree")
			}
			if detach {
				localBranch = ""
			}
			if o.json {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					MergeRequestID string `json:"mergeRequestId"`
					RepositoryID   string `json:"repositoryId"`
					HeadOID        string `json:"headOid"`
					Branch         string `json:"branch"`
					Detached       bool   `json:"detached"`
				}{mr.Id, mr.RepositoryId, oid, localBranch, detach})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Checked out %s at %s\n", printable(mr.Id), oid)
			return err
		},
	}
	t.register(cmd)
	cmd.Flags().StringVarP(&localBranch, "branch", "b", "", "Local branch name (defaults to the MR head branch)")
	cmd.Flags().BoolVar(&detach, "detach", false, "Check out the recorded commit with a detached HEAD")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Reset an existing local branch to the MR head; does not discard uncommitted changes")
	return cmd
}

func cleanCheckout(cmd *cobra.Command) error {
	for _, state := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		path, err := git(cmd, "rev-parse", "--git-path", state)
		if err != nil {
			return fmt.Errorf("cannot inspect the local Git checkout")
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("finish the current merge, rebase or cherry-pick before checking out a merge request")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("cannot inspect the local Git operation")
		}
	}

	status, err := git(cmd, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return fmt.Errorf("cannot read the local Git working tree")
	}
	if status != "" {
		return fmt.Errorf("commit or stash local changes before checking out a merge request")
	}
	return nil
}
