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
	"github.com/spf13/pflag"
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
		Use: "mr", Short: "Manage merge requests", Hidden: true,
		Long: "Manage merge requests using a user token from depot login.\nEvery command infers the Depot repository from local Git remotes.\nPass --repo to select a Depot repository ID instead, and --org if it is not the current organization.",
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.PersistentFlags().StringVar(&o.repo, "repo", "", "Depot repository ID (inferred from Git remotes when omitted)")
	cmd.PersistentFlags().StringVar(&o.org, "org", "", "Organization ID (defaults to the current organization)")
	cmd.PersistentFlags().StringVar(&o.token, "token", "", "Depot user token (or DEPOT_TOKEN)")
	cmd.PersistentFlags().BoolVar(&o.json, "json", false, "Print the API response as JSON")
	cmd.AddCommand(newCreate(o), newView(o), newMerge(o), newApprove(o), newComment(o), newList(o))
	cmd.AddCommand(newEdit(o), newClose(o), newReopen(o))
	cmd.AddCommand(newThreads(o), newReply(o), newResolve(o, true), newResolve(o, false))
	return cmd
}

// authenticate resolves the user token and the repository: --repo, or else the checkout's Git remotes.
func (o *options) authenticate(cmd *cobra.Command) error {
	if err := o.authenticateUser(cmd); err != nil {
		return err
	}
	if o.repo != "" {
		return nil
	}
	repo, err := o.inferRepository(cmd)
	if err != nil {
		return err
	}
	o.repo = repo
	return nil
}

// authenticateFor skips the repository when the merge request is named by ID: no branch lookup needs it.
func (o *options) authenticateFor(cmd *cobra.Command, branch string) error {
	if branch == "" {
		return o.authenticateUser(cmd)
	}
	return o.authenticate(cmd)
}

func (o *options) authenticateUser(cmd *cobra.Command) error {
	if cmd.Flags().Changed("repo") && strings.TrimSpace(o.repo) == "" {
		return fmt.Errorf("--repo must not be blank; provide the Depot repository ID")
	}
	return o.login(cmd)
}

// login is authenticate for the commands that name a comment, which the API finds without a repository.
func (o *options) login(cmd *cobra.Command) error {
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
		ref, err := git(cmd, "symbolic-ref", "--quiet", "HEAD")
		if err != nil || !strings.HasPrefix(ref, "refs/heads/") {
			return "", fmt.Errorf("cannot determine the current branch; check out a branch or specify one explicitly")
		}
		// Abbreviated refs can include "heads/" when a tag shares the branch name.
		name = strings.TrimPrefix(ref, "refs/heads/")
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

// get finds the merge request of a branch: the open one, or else the most recently created closed or merged one.
func (o *options) get(cmd *cobra.Command, name string) (*code.GetMergeRequestResponse, error) {
	return o.find(cmd, name, false)
}

// getOpen is the API's own branch lookup, which finds only an open merge request and is not_found otherwise.
func (o *options) getOpen(cmd *cobra.Command, name string) (*code.GetMergeRequestResponse, error) {
	return o.fetch(cmd, &code.GetMergeRequestRequest{Lookup: o.head(name)})
}

func (o *options) head(name string) *code.GetMergeRequestRequest_Head {
	return &code.GetMergeRequestRequest_Head{Head: &code.GetMergeRequestRequest_HeadRef{RepositoryId: o.repo, HeadRef: name}}
}

func (o *options) find(cmd *cobra.Command, name string, readiness bool) (*code.GetMergeRequestResponse, error) {
	req := &code.GetMergeRequestRequest{Lookup: o.head(name), IncludeReadiness: readiness}
	resp, err := o.fetch(cmd, req)
	if connect.CodeOf(err) != connect.CodeNotFound {
		return resp, err
	}
	newest, listErr := o.newest(cmd, name)
	if listErr != nil {
		return nil, listErr
	}
	if newest == nil {
		return nil, fmt.Errorf("no merge request for branch %q in repository %s: %w", name, o.repo, err)
	}
	// Read it by ID: a listed row is not caught up with its branches.
	req.Lookup = &code.GetMergeRequestRequest_MergeRequestId{MergeRequestId: newest.Id}
	return o.fetch(cmd, req)
}

// newest lists every merge request of a branch and returns an open one, or else the most recently created; nil if none.
func (o *options) newest(cmd *cobra.Command, name string) (*code.MergeRequest, error) {
	req := &code.ListMergeRequestsRequest{RepositoryId: o.repo, HeadRef: name, State: code.ListMergeRequestsRequest_STATE_FILTER_ALL, PageSize: 100}
	var newest *code.MergeRequest
	for {
		resp, err := api.NewMergeRequestClient().ListMergeRequests(cmd.Context(), request(o, req))
		if err != nil {
			return nil, err
		}
		for _, mr := range resp.Msg.MergeRequests {
			if mr.State == code.MergeRequest_STATE_OPEN {
				return mr, nil
			}
			if newest == nil || mr.CreatedAt.AsTime().After(newest.CreatedAt.AsTime()) {
				newest = mr
			}
		}
		if resp.Msg.NextPageToken == "" || resp.Msg.NextPageToken == req.PageToken {
			return newest, nil
		}
		req.PageToken = resp.Msg.NextPageToken
	}
}

// requireState refuses a merge request that a branch lookup found in another state than want.
func requireState(mr *code.MergeRequest, name string, want code.MergeRequest_State) error {
	if mr.State == want {
		return nil
	}
	return fmt.Errorf("merge request %s for branch %q is %s, not %s", mr.Id, name, stateName(mr.State), stateName(want))
}

// getOpenOrRefuse is get for commands that change an open merge request: it names a closed or merged one and stops.
func (o *options) getOpenOrRefuse(cmd *cobra.Command, name string) (*code.GetMergeRequestResponse, error) {
	found, err := o.get(cmd, name)
	if err != nil {
		return nil, err
	}
	return found, requireState(found.MergeRequest, name, code.MergeRequest_STATE_OPEN)
}

func (o *options) fetch(cmd *cobra.Command, req *code.GetMergeRequestRequest) (*code.GetMergeRequestResponse, error) {
	resp, err := api.NewMergeRequestClient().GetMergeRequest(cmd.Context(), request(o, req))
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
	var title, head, base string
	var body bodyFlags
	var attachmentPaths []string
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
			baseRef, err := baseFlag(cmd, base)
			if err != nil {
				return err
			}
			text, _, err := body.read(cmd)
			if err != nil {
				return err
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			assets, err := readAssets(attachmentPaths)
			if err != nil {
				return err
			}
			req := &code.CreateMergeRequestRequest{RepositoryId: o.repo, HeadRef: name, Title: title, Body: text, BaseRef: baseRef}
			if len(assets) == 0 {
				return o.createWith(cmd, req, nil)
			}
			// Only an open merge request is reused; a closed or merged one leaves the branch free for a new one.
			existing, err := o.getOpen(cmd, name)
			if err == nil {
				if err := baseMatches(existing.MergeRequest, baseRef); err != nil {
					return err
				}
				return o.attachToExisting(cmd, existing.MergeRequest, assets)
			}
			if connect.CodeOf(err) != connect.CodeNotFound {
				return err
			}
			return o.saveWithAssets(cmd, name, "", "merge request was not created", assets, func(uploaded, _ []attachment) error {
				return o.createWith(cmd, req, uploaded)
			})
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "Merge request title (required)")
	body.register(cmd, "Merge request description")
	cmd.Flags().StringVar(&head, "head", "", "Source branch (defaults to the current branch)")
	cmd.Flags().StringVar(&base, "base", "", "Destination branch (defaults to the repository default branch)")
	cmd.Flags().SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		if name == "image" {
			name = "attach"
		}
		return pflag.NormalizedName(name)
	})
	cmd.Flags().StringArrayVar(&attachmentPaths, "attach", nil, "Attach a file (repeatable; images, video and common document, text, code and archive files, each up to 100 MiB, at most ten per description; --image is an alias)")
	return cmd
}

// createWith opens the merge request with the uploaded attachments appended to its description.
func (o *options) createWith(cmd *cobra.Command, req *code.CreateMergeRequestRequest, uploaded []attachment) error {
	withRefs := proto.Clone(req).(*code.CreateMergeRequestRequest)
	for _, attachment := range uploaded {
		withRefs.Body += attachment.markdown()
	}
	resp, err := o.create(cmd, withRefs)
	if err != nil {
		return err
	}
	// Create returns an MR opened during the uploads unchanged, without the new references.
	refs := strings.TrimSpace(strings.TrimPrefix(withRefs.Body, req.Body))
	if !strings.Contains(resp.MergeRequest.Body, refs) {
		return fmt.Errorf("merge request %s was opened for this branch during the upload and the attachments were not added; run the command again", resp.MergeRequest.Id)
	}
	if o.json {
		return o.writeJSON(cmd, resp)
	}
	return writeMR(cmd, resp.MergeRequest)
}

// attachToExisting appends attachments to the description of the branch's open
// merge request. CreateMergeRequest returns that MR unchanged, so it cannot attach them.
func (o *options) attachToExisting(cmd *cobra.Command, mr *code.MergeRequest, assets []localAsset) error {
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Branch %s already has merge request %s; adding attachments to its description (title and body text are unchanged)\n", mr.HeadRef, mr.Id)
	unchanged := fmt.Sprintf("merge request %s was not changed", mr.Id)
	return o.saveWithAssets(cmd, mr.HeadRef, mr.Id, unchanged, assets, func(uploaded, earlier []attachment) error {
		return o.appendAttachments(cmd, mr, uploaded, earlier)
	})
}

// appendAttachments adds uploaded to the description. earlier holds the first
// upload of the same files, when uploaded are new copies of them.
func (o *options) appendAttachments(cmd *cobra.Command, mr *code.MergeRequest, uploaded, earlier []attachment) error {
	// Uploads take a while; append to the description as it is now, not as it was before them.
	current, err := o.getOpen(cmd, mr.HeadRef)
	if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
		return fmt.Errorf("attach to merge request %s: %w", mr.Id, err)
	}
	if err != nil || current.MergeRequest.Id != mr.Id {
		return fmt.Errorf("merge request %s is no longer the open merge request for %s; run the command again", mr.Id, mr.HeadRef)
	}
	// A repeated upload returns the same URL, so a re-run adds only what is missing.
	body := current.MergeRequest.Body
	for index, attachment := range uploaded {
		attached := strings.Contains(body, attachment.url) || (index < len(earlier) && strings.Contains(body, earlier[index].url))
		if !attached {
			body += attachment.markdown()
		}
	}
	if body == current.MergeRequest.Body {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Merge request %s already has these attachments; nothing to add\n", mr.Id)
		return o.writeCreated(cmd, current.MergeRequest)
	}
	resp, err := api.NewMergeRequestClient().UpdateMergeRequest(cmd.Context(), request(o, &code.UpdateMergeRequestRequest{
		MergeRequestId: mr.Id, Body: &body,
	}))
	if err != nil {
		return fmt.Errorf("attach to merge request %s: %w", mr.Id, err)
	}
	return o.writeCreated(cmd, resp.Msg.MergeRequest)
}

func (o *options) writeCreated(cmd *cobra.Command, mr *code.MergeRequest) error {
	if o.json {
		return o.writeJSON(cmd, &code.CreateMergeRequestResponse{MergeRequest: mr})
	}
	return writeMR(cmd, mr)
}

func newView(o *options) *cobra.Command {
	var t target
	cmd := &cobra.Command{Use: "view [branch]", Short: "Show a merge request (defaults to the current branch)", Args: cobra.MaximumNArgs(1),
		Long: "Show a merge request.\nA branch, by default the current one, names its open merge request, or else its most recently created closed or merged one.\nPass --id to show any other; `depot mr list --state all --head <branch>` prints the IDs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := t.check(cmd, args)
			if err != nil {
				return err
			}
			if err := o.authenticateFor(cmd, name); err != nil {
				return err
			}
			response, err := o.getWithReadiness(cmd, name, t.id)
			if err != nil {
				return err
			}
			if o.json {
				return o.writeJSON(cmd, response)
			}
			return writeView(cmd, response)
		},
	}
	t.register(cmd)
	return cmd
}

func newMerge(o *options) *cobra.Command {
	var squash bool
	cmd := &cobra.Command{Use: "merge [branch]", Short: "Merge the locally observed head of a branch",
		Long: "Merge an open merge request using the local branch commit as the expected head.\nA newer remote head is refused. Update your local branch and review it before retrying.\nCI results and approvals do not block merging.\nBy default the branch lands as a merge commit. Pass --squash to land it as one commit, with the merge request title and description as its message; your branch is not changed.", Args: cobra.MaximumNArgs(1),
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
			lookup := &code.GetMergeRequestRequest_HeadRef{RepositoryId: o.repo, HeadRef: name}
			client := api.NewMergeRequestClient()
			out := &code.MergeMergeRequestResponse{}
			if squash {
				// Squash has its own method, which an older server refuses instead of silently landing a merge commit.
				resp, err := client.MergeMergeRequestWithMethod(cmd.Context(), request(o, &code.MergeMergeRequestWithMethodRequest{
					Lookup:          &code.MergeMergeRequestWithMethodRequest_Head{Head: lookup},
					ExpectedHeadOid: &head,
					Method:          code.MergeMergeRequestWithMethodRequest_METHOD_SQUASH,
				}))
				if connect.CodeOf(err) == connect.CodeUnimplemented {
					return fmt.Errorf("this server does not support --squash; nothing was merged: %w", err)
				}
				if err != nil {
					return err
				}
				out.MergeRequest = resp.Msg.MergeRequest
			} else {
				resp, err := client.MergeMergeRequest(cmd.Context(), request(o, &code.MergeMergeRequestRequest{
					Lookup:          &code.MergeMergeRequestRequest_Head{Head: lookup},
					ExpectedHeadOid: &head,
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
			if o.json {
				return o.writeJSON(cmd, out)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Merged %s: %s\n", mr.Id, mr.MergeCommitOid)
			return err
		},
	}
	cmd.Flags().BoolVar(&squash, "squash", false, "Land the branch as one commit instead of a merge commit")
	return cmd
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
			mr, err := o.getOpenOrRefuse(cmd, name)
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
	var flags bodyFlags
	cmd := &cobra.Command{Use: "comment [branch] --body <text>", Short: "Post a conversation comment", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _, err := flags.read(cmd)
			if err != nil {
				return err
			}
			if strings.TrimSpace(body) == "" {
				return fmt.Errorf("--body or --body-file is required and must not be blank")
			}
			name, err := branch(cmd, args)
			if err != nil {
				return err
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			mr, err := o.getOpenOrRefuse(cmd, name)
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
	flags.register(cmd, "Comment text (required unless --body-file is set)")
	return cmd
}
