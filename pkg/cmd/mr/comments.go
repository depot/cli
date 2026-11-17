package mr

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

type anchorFlags struct {
	path, side, head, base string
	line, start            uint32
}

func (a *anchorFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&a.path, "path", "", "File path for an inline comment")
	f.StringVar(&a.side, "side", "", "Diff side: old or new")
	f.Uint32Var(&a.line, "line", 0, "Last line of the comment (inclusive)")
	f.Uint32Var(&a.start, "start-line", 0, "First line of a range (defaults to --line)")
	f.StringVar(&a.head, "head-oid", "", "Reviewed head commit from mr view")
	f.StringVar(&a.base, "base-oid", "", "Stored baseOid from mr view, not the diff merge-base; pass an empty value when absent")
}

func (a *anchorFlags) read(cmd *cobra.Command) (*code.MergeRequestCommentAnchor, error) {
	fields := []string{"path", "side", "line", "head-oid", "base-oid"}
	inline := cmd.Flags().Changed("start-line")
	for _, field := range fields {
		inline = inline || cmd.Flags().Changed(field)
	}
	if !inline {
		return nil, nil
	}
	for _, field := range fields {
		if !cmd.Flags().Changed(field) {
			return nil, fmt.Errorf("inline comments require --%s", field)
		}
	}
	if a.path == "" || strings.ContainsRune(a.path, 0) || !utf8.ValidString(a.path) || utf8.RuneCountInString(a.path) > 1024 {
		return nil, fmt.Errorf("--path must be a nonempty UTF-8 path of at most 1024 characters without NUL")
	}
	if !mergeOID.MatchString(a.head) || (a.base != "" && !mergeOID.MatchString(a.base)) {
		return nil, fmt.Errorf("--head-oid and nonempty --base-oid must be lowercase hexadecimal SHA-1 or SHA-256 object IDs")
	}
	side := code.MergeRequestCommentAnchor_SIDE_NEW
	if a.side == "old" {
		side = code.MergeRequestCommentAnchor_SIDE_OLD
	} else if a.side != "new" {
		return nil, fmt.Errorf("--side must be old or new")
	}
	start := a.start
	if !cmd.Flags().Changed("start-line") {
		start = a.line
	}
	if start == 0 || a.line < start {
		return nil, fmt.Errorf("line range must start at 1 or later and end at or after its start")
	}
	return &code.MergeRequestCommentAnchor{Path: a.path, Side: side, StartLine: start, EndLine: a.line, HeadOid: a.head, BaseOid: a.base}, nil
}

func newComment(o *options) *cobra.Command {
	var body bodyFlags
	var t target
	var anchor anchorFlags
	cmd := &cobra.Command{Use: "comment [branch]", Short: "Post a conversation or inline comment", Args: cobra.MaximumNArgs(1),
		Long: "Post a comment on the current branch's open merge request, or use --id without a checkout.\nFor an inline comment pass --path, --side, --line, --head-oid and --base-oid.\nUse the reviewed MR's headOid and stored baseOid from mr view; stale revisions are refused.\n--start-line adds an inclusive range on the same side.",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := t.check(cmd, args)
			if err != nil {
				return err
			}
			a, err := anchor.read(cmd)
			if err != nil {
				return err
			}
			text, _, err := body.read(cmd)
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("--body or --body-file is required and must not be blank")
			}
			if err = o.authenticateFor(cmd, name); err != nil {
				return err
			}
			var found *code.GetMergeRequestResponse
			if name != "" {
				found, err = o.getOpenOrRefuse(cmd, name)
			} else {
				found, err = o.getByID(cmd, t.id)
			}
			if err != nil {
				return err
			}
			mr := found.MergeRequest
			if (t.id != "" && mr.Id != t.id) || (o.repo != "" && mr.RepositoryId != o.repo) {
				return fmt.Errorf("API returned a different merge request or repository")
			}
			resp, err := api.NewMergeRequestClient().CreateMergeRequestComment(cmd.Context(), request(o, &code.CreateMergeRequestCommentRequest{MergeRequestId: mr.Id, Body: text, Anchor: a}))
			if err != nil {
				return err
			}
			c := resp.Msg.Comment
			if c == nil || c.Id == "" || c.MergeRequestId != mr.Id || !proto.Equal(c.Anchor, a) {
				return fmt.Errorf("API did not confirm the requested comment; inspect the merge request before retrying")
			}
			if o.json {
				return o.writeJSON(cmd, resp.Msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Commented on %s: %s\n", printable(c.MergeRequestId), printable(c.Id))
			return err
		},
	}
	t.register(cmd)
	body.register(cmd, "Comment text (required unless --body-file is set)")
	anchor.register(cmd)
	return cmd
}

// Comment IDs are globally scoped; --id optionally verifies their MR before a mutation.
func (o *options) requireComment(cmd *cobra.Command, mrID, id string) error {
	if !cmd.Flags().Changed("id") {
		return nil
	}
	if strings.TrimSpace(mrID) == "" {
		return fmt.Errorf("--id must not be blank")
	}
	found, err := o.getByID(cmd, mrID)
	if err != nil {
		return err
	}
	if found.MergeRequest.Id != mrID || (o.repo != "" && found.MergeRequest.RepositoryId != o.repo) {
		return fmt.Errorf("API returned a different merge request or repository")
	}
	for _, c := range found.Comments {
		if c.Id == id && c.MergeRequestId == mrID && c.DeletedAt == nil {
			return nil
		}
	}
	return fmt.Errorf("comment %s does not belong to merge request %s or was deleted", printable(id), printable(mrID))
}

func newEditComment(o *options) *cobra.Command {
	var body bodyFlags
	var mrID string
	cmd := &cobra.Command{Use: "edit-comment <comment ID>", Short: "Edit your own conversation comment, inline comment or reply", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := commentID(args)
			if err != nil {
				return err
			}
			text, _, err := body.read(cmd)
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("--body or --body-file is required and must not be blank")
			}
			if err = o.authenticateUser(cmd); err != nil {
				return err
			}
			if err = o.requireComment(cmd, mrID, id); err != nil {
				return err
			}
			resp, err := api.NewMergeRequestClient().UpdateMergeRequestComment(cmd.Context(), request(o, &code.UpdateMergeRequestCommentRequest{CommentId: id, Body: text}))
			if err != nil {
				return err
			}
			c := resp.Msg.Comment
			if c == nil || c.Id != id || (mrID != "" && c.MergeRequestId != mrID) {
				return fmt.Errorf("API did not confirm the requested comment edit; inspect the merge request before retrying")
			}
			if o.json {
				return o.writeJSON(cmd, resp.Msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Edited comment %s\n", printable(c.Id))
			return err
		},
	}
	cmd.Flags().StringVar(&mrID, "id", "", "Require the comment to belong to this merge request")
	body.register(cmd, "Updated comment text (required unless --body-file is set)")
	return cmd
}

func newDeleteComment(o *options) *cobra.Command {
	var mrID string
	cmd := &cobra.Command{Use: "delete-comment <comment ID>", Short: "Delete your own conversation comment, inline comment or reply", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := commentID(args)
			if err != nil {
				return err
			}
			if err = o.authenticateUser(cmd); err != nil {
				return err
			}
			if err = o.requireComment(cmd, mrID, id); err != nil {
				return err
			}
			resp, err := api.NewMergeRequestClient().DeleteMergeRequestComment(cmd.Context(), request(o, &code.DeleteMergeRequestCommentRequest{CommentId: id}))
			if err != nil {
				return err
			}
			if o.json {
				return o.writeJSON(cmd, resp.Msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deleted comment %s\n", printable(id))
			return err
		},
	}
	cmd.Flags().StringVar(&mrID, "id", "", "Require the comment to belong to this merge request")
	return cmd
}
