package mr

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

// target selects the merge request a command changes:
// the open merge request of a branch, or any merge request by ID.
type target struct{ id string }

func (t *target) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&t.id, "id", "", "Merge request ID, instead of a branch (required for closed merge requests)")
}

// check validates the selection without calling the API and returns the branch to look up, if any.
func (t *target) check(cmd *cobra.Command, args []string) (string, error) {
	if !cmd.Flags().Changed("id") {
		return branch(cmd, args)
	}
	if len(args) > 0 {
		return "", fmt.Errorf("pass a branch or --id, not both")
	}
	if strings.TrimSpace(t.id) == "" {
		return "", fmt.Errorf("--id must not be blank")
	}
	return "", nil
}

// resolve returns the merge request ID. Call it after check and authenticate.
func (t *target) resolve(cmd *cobra.Command, o *options, name string) (string, error) {
	if name == "" {
		return t.id, nil
	}
	found, err := o.get(cmd, name)
	if connect.CodeOf(err) == connect.CodeNotFound {
		return "", fmt.Errorf("no open merge request for branch %q; closed merge requests are found only by ID, pass --id: %w", name, err)
	}
	if err != nil {
		return "", err
	}
	return found.MergeRequest.Id, nil
}

// update sends one UpdateMergeRequest call and returns the changed merge request.
func (o *options) update(cmd *cobra.Command, req *code.UpdateMergeRequestRequest) (*code.UpdateMergeRequestResponse, error) {
	resp, err := api.NewMergeRequestClient().UpdateMergeRequest(cmd.Context(), request(o, req))
	if err != nil {
		return nil, err
	}
	if resp.Msg.MergeRequest == nil || resp.Msg.MergeRequest.Id == "" {
		return nil, fmt.Errorf("API returned no merge request")
	}
	return resp.Msg, nil
}

func newEdit(o *options) *cobra.Command {
	var title, base string
	var body bodyFlags
	var t target
	cmd := &cobra.Command{Use: "edit [branch]", Short: "Change the title, description or base of a merge request",
		Long: "Change the title, description or base branch of a merge request.\nThe branch defaults to the current branch. Flags that are not passed leave their field unchanged.", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &code.UpdateMergeRequestRequest{}
			if cmd.Flags().Changed("title") {
				if strings.TrimSpace(title) == "" {
					return fmt.Errorf("--title must not be blank")
				}
				req.Title = &title
			}
			name, err := t.check(cmd, args)
			if err != nil {
				return err
			}
			if req.BaseRef, err = baseFlag(cmd, base); err != nil {
				return err
			}
			text, changed, err := body.read(cmd)
			if err != nil {
				return err
			}
			if changed {
				req.Body = &text
			}
			if req.Title == nil && req.Body == nil && req.BaseRef == nil {
				return fmt.Errorf("nothing to change; pass --title, --body, --body-file or --base")
			}
			if err := o.authenticateFor(cmd, name); err != nil {
				return err
			}
			if req.MergeRequestId, err = t.resolve(cmd, o, name); err != nil {
				return err
			}
			resp, err := o.update(cmd, req)
			if err != nil {
				return err
			}
			if o.json {
				return o.writeJSON(cmd, resp)
			}
			return writeMR(cmd, resp.MergeRequest)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "New title")
	body.register(cmd, "New description (an empty value clears it)")
	cmd.Flags().StringVar(&base, "base", "", "New destination branch")
	t.register(cmd)
	return cmd
}
