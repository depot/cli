package mr

import (
	"fmt"
	"os/exec"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type options struct {
	org, token, repo string
	json             bool
}

func NewCmdMR() *cobra.Command {
	o := &options{}
	cmd := &cobra.Command{
		Use: "mr", Short: "Manage merge requests",
		Long: "Manage merge requests using a user token from depot login.\nPass --repo with the Depot repository ID and --org if it is not the current organization.",
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.PersistentFlags().StringVar(&o.repo, "repo", "", "Depot repository ID (required)")
	cmd.PersistentFlags().StringVar(&o.org, "org", "", "Organization ID (defaults to the current organization)")
	cmd.PersistentFlags().StringVar(&o.token, "token", "", "Depot user token (or DEPOT_TOKEN)")
	cmd.PersistentFlags().BoolVar(&o.json, "json", false, "Print the API response as JSON")
	cmd.AddCommand(newCreate(o), newView(o), newMerge(o), newApprove(o), newComment(o))
	return cmd
}

func (o *options) authenticate(cmd *cobra.Command) error {
	if strings.TrimSpace(o.repo) == "" {
		return fmt.Errorf("--repo is required; provide the Depot repository ID")
	}
	if o.org == "" {
		o.org = config.GetCurrentOrganization()
	}
	var err error
	o.token, err = helpers.ResolveOrgAuth(cmd.Context(), o.token)
	if err != nil {
		return err
	}
	if o.token == "" {
		return fmt.Errorf("missing user token; run `depot login` or pass --token or DEPOT_TOKEN")
	}
	return nil
}

func request[T any](o *options, message *T) *connect.Request[T] {
	return api.WithAuthenticationAndOrg(connect.NewRequest(message), o.token, o.org)
}

func (o *options) writeJSON(cmd *cobra.Command, message proto.Message) error {
	data, err := (protojson.MarshalOptions{Indent: "  "}).Marshal(message)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}

func git(cmd *cobra.Command, args ...string) (string, error) {
	out, err := exec.CommandContext(cmd.Context(), "git", args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func branch(cmd *cobra.Command, args []string) (string, error) {
	var name string
	if len(args) > 0 {
		name = args[0]
	} else {
		var err error
		name, err = git(cmd, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil {
			return "", fmt.Errorf("cannot determine the current branch; check out a branch or specify one explicitly")
		}
	}
	// Check the full ref without interpreting revision expressions or @{-1}.
	if name == "" || strings.HasPrefix(name, "-") {
		return "", fmt.Errorf("invalid branch name %q", name)
	}
	if _, err := git(cmd, "check-ref-format", "refs/heads/"+name); err != nil {
		return "", fmt.Errorf("invalid branch name %q", name)
	}
	return name, nil
}

func (o *options) get(cmd *cobra.Command, name string) (*code.GetMergeRequestResponse, error) {
	resp, err := api.NewMergeRequestClient().GetMergeRequest(cmd.Context(), request(o, &code.GetMergeRequestRequest{
		Lookup: &code.GetMergeRequestRequest_Head{Head: &code.GetMergeRequestRequest_HeadRef{RepositoryId: o.repo, HeadRef: name}},
	}))
	if err != nil {
		return nil, err
	}
	if resp.Msg.MergeRequest == nil || resp.Msg.MergeRequest.Id == "" {
		return nil, fmt.Errorf("API returned no merge request")
	}
	return resp.Msg, nil
}

func writeMR(cmd *cobra.Command, mr *code.MergeRequest) error {
	if mr == nil || mr.Id == "" {
		return fmt.Errorf("API returned no merge request")
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\nState: %s\nHead: %s (%s)\nBase: %s\n", mr.Id, mr.Title,
		strings.ToLower(strings.TrimPrefix(mr.State.String(), "STATE_")), mr.HeadRef, mr.HeadOid, mr.BaseRef)
	if err != nil {
		return err
	}
	if mr.HeadBranchDeleted {
		if _, err = fmt.Fprintln(cmd.OutOrStdout(), "Head branch deleted"); err != nil {
			return err
		}
	}
	if mr.Body != "" {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", mr.Body)
	}
	return err
}

func newCreate(o *options) *cobra.Command {
	var title, body, head string
	cmd := &cobra.Command{Use: "create", Short: "Open a merge request for the current branch", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(title) == "" {
				return fmt.Errorf("--title is required and must not be blank")
			}
			var args []string
			if cmd.Flags().Changed("head") {
				args = []string{head}
			}
			name, err := branch(cmd, args)
			if err != nil {
				return err
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			resp, err := api.NewMergeRequestClient().CreateMergeRequest(cmd.Context(), request(o, &code.CreateMergeRequestRequest{
				RepositoryId: o.repo, HeadRef: name, Title: title, Body: body,
			}))
			if err != nil {
				return err
			}
			if resp.Msg.MergeRequest == nil || resp.Msg.MergeRequest.Id == "" {
				return fmt.Errorf("API returned no merge request")
			}
			if o.json {
				return o.writeJSON(cmd, resp.Msg)
			}
			return writeMR(cmd, resp.Msg.MergeRequest)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "Merge request title (required)")
	cmd.Flags().StringVar(&body, "body", "", "Merge request description")
	cmd.Flags().StringVar(&head, "head", "", "Source branch (defaults to the current branch)")
	return cmd
}

func newView(o *options) *cobra.Command {
	return &cobra.Command{Use: "view [branch]", Short: "Show a merge request (defaults to the current branch)", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := branch(cmd, args)
			if err != nil {
				return err
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			response, err := o.get(cmd, name)
			if err != nil {
				return err
			}
			if o.json {
				return o.writeJSON(cmd, response)
			}
			return writeMR(cmd, response.MergeRequest)
		},
	}
}

func newMerge(o *options) *cobra.Command {
	return &cobra.Command{Use: "merge [branch]", Short: "Merge the locally observed head of a branch",
		Long: "Merge an open merge request using the local branch commit as the expected head.\nA newer remote head is refused. Update your local branch and review it before retrying.\nCI results and approvals do not block merging.", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := branch(cmd, args)
			if err != nil {
				return err
			}
			head, err := git(cmd, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+name+"^{commit}")
			if err != nil {
				return fmt.Errorf("cannot resolve local branch %q; check it out before merging", name)
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			resp, err := api.NewMergeRequestClient().MergeMergeRequest(cmd.Context(), request(o, &code.MergeMergeRequestRequest{
				Lookup:          &code.MergeMergeRequestRequest_Head{Head: &code.GetMergeRequestRequest_HeadRef{RepositoryId: o.repo, HeadRef: name}},
				ExpectedHeadOid: &head,
			}))
			if err != nil {
				return err
			}
			mr := resp.Msg.MergeRequest
			if mr == nil || mr.Id == "" || mr.State != code.MergeRequest_STATE_MERGED || mr.MergeCommitOid == "" {
				return fmt.Errorf("API returned no completed merge result")
			}
			if o.json {
				return o.writeJSON(cmd, resp.Msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Merged %s: %s\n", mr.Id, mr.MergeCommitOid)
			return err
		},
	}
}

func newApprove(o *options) *cobra.Command {
	return &cobra.Command{Use: "approve [branch]", Short: "Approve a merge request at its current head", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := branch(cmd, args)
			if err != nil {
				return err
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			mr, err := o.get(cmd, name)
			if err != nil {
				return err
			}
			resp, err := api.NewMergeRequestClient().ApproveMergeRequest(cmd.Context(), request(o, &code.ApproveMergeRequestRequest{MergeRequestId: mr.MergeRequest.Id}))
			if err != nil {
				return err
			}
			approval := resp.Msg.Approval
			if approval == nil || approval.MergeRequestId == "" || approval.HeadOid == "" {
				return fmt.Errorf("API returned no approval")
			}
			if o.json {
				return o.writeJSON(cmd, resp.Msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Approved %s at %s\n", approval.MergeRequestId, approval.HeadOid)
			return err
		},
	}
}

func newComment(o *options) *cobra.Command {
	var body string
	cmd := &cobra.Command{Use: "comment [branch] --body <text>", Short: "Post a conversation comment", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(body) == "" {
				return fmt.Errorf("--body is required and must not be blank")
			}
			name, err := branch(cmd, args)
			if err != nil {
				return err
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			mr, err := o.get(cmd, name)
			if err != nil {
				return err
			}
			resp, err := api.NewMergeRequestClient().CreateMergeRequestComment(cmd.Context(), request(o, &code.CreateMergeRequestCommentRequest{MergeRequestId: mr.MergeRequest.Id, Body: body}))
			if err != nil {
				return err
			}
			comment := resp.Msg.Comment
			if comment == nil || comment.Id == "" {
				return fmt.Errorf("API returned no comment")
			}
			if o.json {
				return o.writeJSON(cmd, resp.Msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Commented on %s: %s\n", comment.MergeRequestId, comment.Id)
			return err
		},
	}
	cmd.Flags().StringVar(&body, "body", "", "Comment text (required)")
	return cmd
}
