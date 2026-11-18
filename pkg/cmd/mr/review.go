package mr

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

var reviewKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func newReview(o *options) *cobra.Command {
	var t target
	var body bodyFlags
	var approve, changes, comment bool
	var head, key string
	cmd := &cobra.Command{Use: "review [branch]", Short: "Submit a comment, approval, or request-changes review", Args: cobra.MaximumNArgs(1),
		Long: "Submit a human review of an open merge request. Choose --comment, --approve or --request-changes.\nThe reviewed head defaults to the current head; --expected-head-oid pins it explicitly.\nReuse --request-key and --expected-head-oid with the same body and verdict after an uncertain response.\nRequesting changes replaces your prior verdict but does not block merging.",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := t.check(cmd, args)
			if err != nil {
				return err
			}
			verdict, count := code.MergeRequestReview_VERDICT_UNSPECIFIED, 0
			for _, choice := range []struct {
				set   bool
				value code.MergeRequestReview_Verdict
			}{{approve, code.MergeRequestReview_VERDICT_APPROVE}, {changes, code.MergeRequestReview_VERDICT_REQUEST_CHANGES}, {comment, code.MergeRequestReview_VERDICT_COMMENT}} {
				if choice.set {
					verdict = choice.value
					count++
				}
			}
			if count != 1 {
				return fmt.Errorf("choose exactly one of --approve, --request-changes or --comment")
			}
			text, _, err := body.read(cmd)
			if err != nil {
				return err
			}
			if verdict != code.MergeRequestReview_VERDICT_APPROVE && strings.TrimSpace(text) == "" {
				return fmt.Errorf("--body or --body-file is required for this review")
			}
			if cmd.Flags().Changed("expected-head-oid") && !mergeOID.MatchString(head) {
				return fmt.Errorf("--expected-head-oid must be a lowercase hexadecimal SHA-1 or SHA-256 object ID")
			}
			if cmd.Flags().Changed("request-key") && !reviewKeyPattern.MatchString(key) {
				return fmt.Errorf("--request-key must contain 1-128 ASCII letters, digits, hyphens or underscores")
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
			// An explicit retry can recover a persisted review after the MR closes; the API refuses new writes.
			if !cmd.Flags().Changed("request-key") || !cmd.Flags().Changed("expected-head-oid") {
				if err := requireState(mr, name, code.MergeRequest_STATE_OPEN); err != nil {
					return err
				}
			}
			if head == "" {
				head = mr.HeadOid
			}
			if !mergeOID.MatchString(head) {
				return fmt.Errorf("API returned no valid merge request head")
			}
			if key == "" {
				var random [16]byte
				if _, err := rand.Read(random[:]); err != nil {
					return err
				}
				key = hex.EncodeToString(random[:])
			}
			response, err := api.NewMergeRequestClient().SubmitMergeRequestReview(cmd.Context(), request(o, &code.SubmitMergeRequestReviewRequest{MergeRequestId: mr.Id, ExpectedHeadOid: head, Verdict: verdict, Body: text, RequestKey: key}))
			if err != nil {
				return fmt.Errorf("review failed; to retry this submission reuse --request-key %s --expected-head-oid %s with the same body and verdict: %w", key, head, err)
			}
			review := response.Msg.Review
			if review == nil || review.Id == "" || review.MergeRequestId != mr.Id || review.HeadOid != head || review.Verdict != verdict || review.Body != text {
				return fmt.Errorf("API did not confirm the requested review; inspect reviews before retrying with --request-key %s --expected-head-oid %s", key, head)
			}
			if o.json {
				return o.writeJSON(cmd, response.Msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Review %s: %s at %s%s\n", printable(review.Id), review.Verdict, printable(review.HeadOid), reviewStatus(review))
			return err
		},
	}
	t.register(cmd)
	body.register(cmd, "Review summary")
	cmd.Flags().BoolVar(&approve, "approve", false, "Approve the reviewed commit")
	cmd.Flags().BoolVar(&changes, "request-changes", false, "Request changes to the reviewed commit")
	cmd.Flags().BoolVar(&comment, "comment", false, "Comment without changing your verdict")
	cmd.Flags().StringVar(&head, "expected-head-oid", "", "Submit only for this reviewed commit")
	cmd.Flags().StringVar(&key, "request-key", "", "Retry key (generated when omitted; reuse after an uncertain response)")
	return cmd
}

func newReviews(o *options) *cobra.Command {
	var t target
	var limit uint32
	var page string
	cmd := &cobra.Command{Use: "reviews [branch]", Short: "List one page of human review history", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		name, err := t.check(cmd, args)
		if err != nil {
			return err
		}
		if limit < 1 || limit > 100 {
			return fmt.Errorf("--limit must be between 1 and 100")
		}
		if err := o.authenticateFor(cmd, name); err != nil {
			return err
		}
		id := t.id
		if name != "" {
			found, err := o.get(cmd, name)
			if err != nil {
				return err
			}
			id = found.MergeRequest.Id
		}
		response, err := api.NewMergeRequestClient().ListMergeRequestReviews(cmd.Context(), request(o, &code.ListMergeRequestReviewsRequest{MergeRequestId: id, PageSize: limit, PageToken: page}))
		if err != nil {
			return err
		}
		for _, r := range response.Msg.Reviews {
			if r == nil || r.MergeRequestId != id {
				return fmt.Errorf("API returned a review for another merge request")
			}
		}
		if o.json {
			return o.writeJSON(cmd, response.Msg)
		}
		for _, r := range response.Msg.Reviews {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s%s\t%s\t%s\t%s\n", printable(r.Id), r.Verdict, reviewStatus(r), printable(r.GetReviewer().GetId()), printable(r.HeadOid), printable(r.Body)); err != nil {
				return err
			}
		}
		if response.Msg.NextPageToken != "" {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Next page: --page-token %s\n", printable(response.Msg.NextPageToken))
		}
		return err
	}}
	t.register(cmd)
	cmd.Flags().Uint32Var(&limit, "limit", 50, "Reviews per page (1-100)")
	cmd.Flags().StringVar(&page, "page-token", "", "Token returned by the previous page")
	return cmd
}

func newWithdrawReview(o *options) *cobra.Command {
	var id string
	cmd := &cobra.Command{Use: "withdraw-review <review-id>", Short: "Withdraw your own review, retaining its history", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(args[0]) == "" {
			return fmt.Errorf("--id and a review ID are required")
		}
		if err := o.authenticateUser(cmd); err != nil {
			return err
		}
		response, err := api.NewMergeRequestClient().WithdrawMergeRequestReview(cmd.Context(), request(o, &code.WithdrawMergeRequestReviewRequest{MergeRequestId: id, ReviewId: args[0]}))
		if err != nil {
			return err
		}
		review := response.Msg.Review
		if review == nil || review.Id != args[0] || review.MergeRequestId != id || review.WithdrawnAt == nil {
			return fmt.Errorf("API did not confirm review withdrawal")
		}
		if o.json {
			return o.writeJSON(cmd, response.Msg)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Withdrew review %s\n", printable(review.Id))
		return err
	}}
	cmd.Flags().StringVar(&id, "id", "", "Merge request ID")
	return cmd
}

func reviewStatus(review *code.MergeRequestReview) string {
	if review.WithdrawnAt != nil {
		return " (withdrawn)"
	}
	if review.SupersededAt != nil {
		return " (superseded)"
	}
	return ""
}
