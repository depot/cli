package checks

import (
	"fmt"
	"slices"

	"github.com/depot/cli/pkg/api"
	checksv1 "github.com/depot/cli/pkg/proto/depot/checks/v1"
	"github.com/spf13/cobra"
)

func newList(o *options) *cobra.Command {
	p := &pagination{}
	req := &checksv1.ListChecksRequest{}
	var repo string
	cmd := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List checks across review agents", Args: cobra.NoArgs,
		Example: "  depot checks list --repo github:depot/api --branch main\n  depot checks list --outcome failed --all --output json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := p.validate(); err != nil {
				return err
			}
			if repo != "" {
				var err error
				req.Repo, err = parseRepo(repo)
				if err != nil {
					return err
				}
			}
			if req.Branch != "" && req.Repo == nil {
				return fmt.Errorf("--branch requires --repo")
			}
			if req.Outcome != "" && !slices.Contains([]string{"running", "done", "passed", "findings", "rejected", "resolved", "failed"}, req.Outcome) {
				return fmt.Errorf("invalid --outcome %q", req.Outcome)
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			client := api.NewCheckClient()
			result := &checksv1.ListChecksResponse{}
			seen := map[string]bool{p.token: true}
			for {
				req.PageSize, req.PageToken = uint32(p.n), p.token
				resp, err := client.ListChecks(cmd.Context(), request(o, req))
				if err != nil {
					return err
				}
				result.Checks = append(result.Checks, resp.Msg.Checks...)
				result.NextPageToken = resp.Msg.NextPageToken
				next, err := p.next(result.NextPageToken, seen)
				if err != nil {
					return err
				}
				if !next {
					break
				}
			}
			if o.output == "json" {
				return o.writeJSON(cmd, result)
			}
			pageHint(cmd, result.NextPageToken)
			attempts := make([]*checksv1.CheckAttempt, 0, len(result.Checks))
			for _, check := range result.Checks {
				attempts = append(attempts, check.CurrentAttempt)
			}
			return printAttempts(cmd, attempts)
		},
	}
	p.flags(cmd)
	cmd.Flags().StringVar(&req.AgentId, "agent", "", "Filter by review agent ID")
	cmd.Flags().StringVar(&repo, "repo", "", "Filter by <forge>:<repository>, e.g. github:depot/api")
	cmd.Flags().StringVar(&req.Branch, "branch", "", "Exact branch name (requires --repo)")
	cmd.Flags().StringVar(&req.Sha, "sha", "", "Head or base commit SHA prefix")
	cmd.Flags().StringVar(&req.Outcome, "outcome", "", "Filter: running, done, passed, findings, rejected, resolved, failed")
	cmd.Flags().StringVar(&req.Query, "query", "", "Search agent names, repositories, branches, and SHA prefixes")
	return cmd
}

func newGet(o *options) *cobra.Command {
	return &cobra.Command{
		Use: "get <check-id | attempt-id>", Aliases: []string{"show"}, Short: "Show a check's latest attempt or an exact execution", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			attempt, err := selectAttempt(cmd.Context(), o, args[0])
			if err != nil {
				return err
			}
			if o.output == "json" {
				return o.writeJSON(cmd, &checksv1.GetAttemptResponse{Attempt: attempt})
			}
			return printAttempt(cmd, attempt)
		},
	}
}

func newAttempts(o *options) *cobra.Command {
	p := &pagination{}
	cmd := &cobra.Command{
		Use: "attempts <check-id | attempt-id>", Short: "List execution history for a check", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := p.validate(); err != nil {
				return err
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			selected, err := selectAttempt(cmd.Context(), o, args[0])
			if err != nil {
				return err
			}
			result := &checksv1.ListAttemptsResponse{}
			client := api.NewCheckClient()
			seen := map[string]bool{p.token: true}
			for {
				resp, err := client.ListAttempts(cmd.Context(), request(o, &checksv1.ListAttemptsRequest{CheckId: selected.CheckId, PageSize: uint32(p.n), PageToken: p.token}))
				if err != nil {
					return err
				}
				result.Attempts = append(result.Attempts, resp.Msg.Attempts...)
				result.NextPageToken = resp.Msg.NextPageToken
				next, err := p.next(result.NextPageToken, seen)
				if err != nil {
					return err
				}
				if !next {
					break
				}
			}
			if o.output == "json" {
				return o.writeJSON(cmd, result)
			}
			pageHint(cmd, result.NextPageToken)
			return printAttempts(cmd, result.Attempts)
		},
	}
	p.flags(cmd)
	return cmd
}

func newRun(o *options) *cobra.Command {
	var repo, ref, commit, base, head string
	cmd := &cobra.Command{
		Use: "run <agent-id>", Short: "Queue a check without changing repository watches", Args: cobra.ExactArgs(1),
		Example: "  depot checks run <agent-id> --repo github:depot/api --ref main\n  depot checks run <agent-id> --repo code:tools --base main --head feature",
		RunE: func(cmd *cobra.Command, args []string) error {
			selector, err := parseRepo(repo)
			if err != nil {
				return err
			}
			req := &checksv1.RunCheckRequest{AgentId: args[0], Repo: selector}
			for name, dest := range map[string]**string{"ref": &req.Ref, "commit-sha": &req.CommitSha, "base": &req.Base, "head": &req.Head} {
				if cmd.Flags().Changed(name) {
					value, _ := cmd.Flags().GetString(name)
					if value == "" {
						return fmt.Errorf("--%s cannot be empty", name)
					}
					*dest = &value
				}
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			resp, err := api.NewCheckClient().RunCheck(cmd.Context(), request(o, req))
			if err != nil {
				return err
			}
			if o.output == "json" {
				return o.writeJSON(cmd, resp.Msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Check:   %s\nAttempt: %s\nQueued. Inspect with: depot checks get %s\n", resp.Msg.CheckId, resp.Msg.AttemptId, resp.Msg.AttemptId)
			return err
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "Repository to review: <forge>:<name> (required; need not be watched)")
	cmd.Flags().StringVar(&ref, "ref", "", "Branch or tag (defaults to the default branch's HEAD)")
	cmd.Flags().StringVar(&commit, "commit-sha", "", "Full head commit SHA; may be combined with --ref for branch context")
	cmd.Flags().StringVar(&base, "base", "", "Comparison base ref or full SHA (defaults to head's first parent)")
	cmd.Flags().StringVar(&head, "head", "", "Comparison head ref or full SHA")
	cmd.MarkFlagsMutuallyExclusive("head", "ref")
	cmd.MarkFlagsMutuallyExclusive("head", "commit-sha")
	return cmd
}

func newRetry(o *options) *cobra.Command {
	return &cobra.Command{
		Use: "retry <check-id | attempt-id>", Short: "Retry a check whose current execution failed", Args: cobra.ExactArgs(1),
		Long: "Queue another attempt using the check's captured configuration and revisions. Only the current, execution-failed attempt is eligible.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			attempt, err := selectAttempt(cmd.Context(), o, args[0])
			if err != nil {
				return err
			}
			if !attempt.IsCurrent {
				return fmt.Errorf("attempt %s is superseded; inspect check %s for its current attempt", attempt.Id, attempt.CheckId)
			}
			if attempt.Status != "failed" {
				return fmt.Errorf("only execution-failed checks can be retried (status: %s)", attempt.Status)
			}
			resp, err := api.NewCheckClient().RetryCheck(cmd.Context(), request(o, &checksv1.RetryCheckRequest{CheckId: attempt.CheckId}))
			if err != nil {
				return err
			}
			if o.output == "json" {
				return o.writeJSON(cmd, resp.Msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Check:   %s\nAttempt: %s\nQueued. Inspect with: depot checks get %s\n", resp.Msg.CheckId, resp.Msg.AttemptId, resp.Msg.AttemptId)
			return err
		},
	}
}

func newResolve(o *options, resolve bool) *cobra.Command {
	var indexes []uint
	var all bool
	name := "resolve"
	if !resolve {
		name = "unresolve"
	}
	cmd := &cobra.Command{
		Use: name + " <check-id | attempt-id>", Short: name + " selected findings on the current attempt", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(indexes) > 0) {
				return fmt.Errorf("supply either --finding <index> (repeatable) or --all")
			}
			findings := make([]uint32, 0, len(indexes))
			for _, index := range indexes {
				if uint64(index) > uint64(^uint32(0)) {
					return fmt.Errorf("finding index is out of range")
				}
				findings = append(findings, uint32(index))
			}
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			attempt, err := selectAttempt(cmd.Context(), o, args[0])
			if err != nil {
				return err
			}
			if !attempt.IsCurrent {
				return fmt.Errorf("attempt %s is superseded; historical findings cannot be changed", attempt.Id)
			}
			client := api.NewCheckClient()
			var check *checksv1.Check
			if resolve {
				resp, err := client.ResolveFindings(cmd.Context(), request(o, &checksv1.ResolveFindingsRequest{CheckId: attempt.CheckId, AttemptId: attempt.Id, FindingIndexes: findings, All: all}))
				if err != nil {
					return err
				}
				check = resp.Msg.Check
			} else {
				resp, err := client.UnresolveFindings(cmd.Context(), request(o, &checksv1.UnresolveFindingsRequest{CheckId: attempt.CheckId, AttemptId: attempt.Id, FindingIndexes: findings, All: all}))
				if err != nil {
					return err
				}
				check = resp.Msg.Check
			}
			if o.output == "json" {
				return o.writeJSON(cmd, &checksv1.GetCheckResponse{Check: check})
			}
			return printAttempt(cmd, check.GetCurrentAttempt())
		},
	}
	cmd.Flags().UintSliceVar(&indexes, "finding", nil, "Original zero-based finding index (repeatable or comma-separated)")
	cmd.Flags().BoolVar(&all, "all", false, "Apply to all findings")
	return cmd
}
