package mr

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

var listStates = map[string]code.ListMergeRequestsRequest_StateFilter{
	"open":   code.ListMergeRequestsRequest_STATE_FILTER_OPEN,
	"closed": code.ListMergeRequestsRequest_STATE_FILTER_CLOSED,
	"merged": code.ListMergeRequestsRequest_STATE_FILTER_MERGED,
	"all":    code.ListMergeRequestsRequest_STATE_FILTER_ALL,
}

func newList(o *options) *cobra.Command {
	var state, head, base, author, pageToken string
	var limit uint32
	cmd := &cobra.Command{Use: "list", Short: "List merge requests, most recently updated first", Args: cobra.NoArgs,
		Long: "List a repository's merge requests, most recently updated first.\n--author takes a user ID, agent:<agent ID>, or @me for merge requests you opened.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &code.ListMergeRequestsRequest{RepositoryId: o.repo, HeadRef: head, BaseRef: base, PageSize: limit, PageToken: pageToken}
			var ok bool
			if req.State, ok = listStates[strings.ToLower(state)]; !ok {
				return fmt.Errorf("invalid --state %q; use open, closed, merged or all", state)
			}
			switch {
			case author == "":
			case author == "@me":
				req.AuthoredByMe = true
			case strings.HasPrefix(author, "agent:"):
				req.Author = &code.MergeRequestActor{Kind: code.MergeRequestActor_KIND_AGENT, Id: strings.TrimPrefix(author, "agent:")}
			default:
				req.Author = &code.MergeRequestActor{Kind: code.MergeRequestActor_KIND_USER, Id: author}
			}
			if req.Author != nil && req.Author.Id == "" {
				return fmt.Errorf("invalid --author %q; use a user ID, agent:<agent ID> or @me", author)
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			resp, err := api.NewMergeRequestClient().ListMergeRequests(cmd.Context(), request(o, req))
			if err != nil {
				return err
			}
			if o.json {
				return o.writeJSON(cmd, resp.Msg)
			}
			return writeMRList(cmd, resp.Msg)
		},
	}
	cmd.Flags().StringVar(&state, "state", "open", "Merge request state: open, closed, merged or all")
	cmd.Flags().StringVar(&head, "head", "", "Only merge requests from this source branch")
	cmd.Flags().StringVar(&base, "base", "", "Only merge requests into this target branch")
	cmd.Flags().StringVar(&author, "author", "", "Only merge requests opened by this user ID, agent:<agent ID>, or @me")
	cmd.Flags().Uint32Var(&limit, "limit", 0, "Maximum merge requests to return (server default 30, at most 100)")
	cmd.Flags().StringVar(&pageToken, "page-token", "", "Continue a previous listing from its next page token")
	return cmd
}

func writeMRList(cmd *cobra.Command, resp *code.ListMergeRequestsResponse) error {
	out := cmd.OutOrStdout()
	if len(resp.MergeRequests) == 0 {
		if _, err := fmt.Fprintln(out, "No merge requests found"); err != nil {
			return err
		}
	}
	for _, mr := range resp.MergeRequests {
		state := strings.ToLower(strings.TrimPrefix(mr.State.String(), "STATE_"))
		_, err := fmt.Fprintf(out, "%s\t%s\t%s -> %s\t%s\n", mr.Id, state, printable(mr.HeadRef), printable(mr.BaseRef), printable(mr.Title))
		if err != nil {
			return err
		}
	}
	if resp.NextPageToken == "" {
		return nil
	}
	// The hint goes to stderr so stdout stays one merge request per line.
	_, err := fmt.Fprintf(cmd.ErrOrStderr(), "More merge requests: rerun with --page-token %s\n", resp.NextPageToken)
	return err
}

// printable replaces control and format characters, such as newlines and bidirectional overrides,
// so a title or branch name cannot break or visually reorder a row.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
}
