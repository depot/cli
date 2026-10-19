package jobs

import (
	"fmt"
	"strings"
	"time"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type options struct {
	org, token, output string
}

type pageOptions struct {
	start, end, token string
	size              int32
}

func (o *options) flags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&o.org, "org", "", "Organization ID (defaults to the current organization)")
	f.StringVar(&o.token, "token", "", "Depot API token")
	f.StringVar(&o.output, "output", "auto", "Output format (auto, table, json)")
}

func (o *options) resolve(cmd *cobra.Command) error {
	switch o.output {
	case "auto":
		o.output = "json"
		if helpers.IsTerminal() {
			o.output = "table"
		}
	case "table", "json":
	default:
		return fmt.Errorf("--output must be auto, table, or json")
	}
	if o.org == "" {
		o.org = config.GetCurrentOrganization()
	}
	if o.output == "json" {
		o.token = helpers.ResolveStaticOrgAuth(o.token)
	} else {
		var err error
		o.token, err = helpers.ResolveOrgAuth(cmd.Context(), o.token)
		if err != nil {
			return err
		}
	}
	if o.token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}
	return nil
}

func (p *pageOptions) flags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&p.start, "start", "", "Inclusive start time (RFC 3339; requires --end)")
	f.StringVar(&p.end, "end", "", "Exclusive end time (RFC 3339; requires --start)")
	f.Int32Var(&p.size, "page-size", 50, "Results per page (1-250)")
	f.StringVar(&p.token, "page-token", "", "Next page token; keep filters and time range unchanged")
}

func (p *pageOptions) timeRange() (*corev1.GithubActionsTimeRange, error) {
	if p.size < 1 || p.size > 250 {
		return nil, fmt.Errorf("--page-size must be between 1 and 250")
	}
	if p.start == "" && p.end == "" {
		return nil, nil
	}
	if p.start == "" || p.end == "" {
		return nil, fmt.Errorf("--start and --end must be supplied together")
	}
	start, err := time.Parse(time.RFC3339, p.start)
	if err != nil {
		return nil, fmt.Errorf("--start must be an RFC 3339 timestamp")
	}
	end, err := time.Parse(time.RFC3339, p.end)
	if err != nil {
		return nil, fmt.Errorf("--end must be an RFC 3339 timestamp")
	}
	tr := &corev1.GithubActionsTimeRange{StartAt: timestamppb.New(start), EndAt: timestamppb.New(end)}
	if tr.StartAt.CheckValid() != nil || tr.EndAt.CheckValid() != nil {
		return nil, fmt.Errorf("timestamps must be between years 0001 and 9999")
	}
	if !end.After(start) || end.Sub(start) > 90*24*time.Hour {
		return nil, fmt.Errorf("time range must be positive and at most 90 days")
	}
	return tr, nil
}

// NewCmdJobs lists jobs by default, or retrieves one job by ID.
func NewCmdJobs() *cobra.Command {
	cmd := newCmdJobs(true)
	cmd.AddCommand(newCmdAnalytics())
	return cmd
}

// NewCmdListJobs provides the same listing under depot list jobs.
func NewCmdListJobs() *cobra.Command { return newCmdJobs(false) }

func newCmdJobs(allowGet bool) *cobra.Command {
	var o options
	var p pageOptions
	var repos, statuses, conclusions, labels []string
	var query string
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "List GitHub Actions jobs run on Depot",
		Long: `List GitHub Actions jobs newest first, one page at a time.
The default creation window is the last 30 days; the maximum is 90 days.
JSON is automatic when stdout is piped and does not prompt for login.
Use the returned nextPageToken with --page-token to retrieve another page.`,
		Args: cobra.NoArgs,
	}
	if allowGet {
		cmd.Use = "jobs [job-id]"
		cmd.Short = "List or inspect GitHub Actions jobs run on Depot"
		cmd.Args = cobra.MaximumNArgs(1)
		cmd.Long += "\n\nSupply a job ID and one --repo to inspect a job. Step timings are not\navailable from the API. Use jobs analytics for aggregate CPU and memory metrics."
	}
	if allowGet {
		cmd.Example = `  depot jobs --repo acme/api --output json
  depot jobs 123456789 --repo acme/api
  depot jobs analytics --repo acme/api`
	} else {
		cmd.Example = `  depot list jobs --repo acme/api --output json`
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			if len(repos) != 1 || strings.TrimSpace(repos[0]) == "" {
				return fmt.Errorf("a job ID requires exactly one --repo owner/name")
			}
			for _, flag := range []string{"status", "conclusion", "runner-label", "query", "start", "end", "page-size", "page-token"} {
				if cmd.Flags().Changed(flag) {
					return fmt.Errorf("--%s is only supported when listing jobs", flag)
				}
			}
			if err := o.resolve(cmd); err != nil {
				return err
			}
			resp, err := api.GetGithubActionsJob(cmd.Context(), o.token, o.org, &corev1.GetGithubActionsJobRequest{Repository: repos[0], JobId: args[0]})
			if err != nil {
				return fmt.Errorf("failed to get GitHub Actions job: %w", err)
			}
			if resp.Job == nil {
				return fmt.Errorf("GitHub Actions job response contained no job")
			}
			if o.output == "json" {
				return writeJSON(cmd.OutOrStdout(), resp)
			}
			return writeJob(cmd.OutOrStdout(), resp.Job)
		}
		tr, err := p.timeRange()
		if err != nil {
			return err
		}
		req := &corev1.ListGithubActionsJobsRequest{Repositories: repos, RunnerLabels: labels, TimeRange: tr, PageSize: proto.Int32(p.size)}
		if p.token != "" {
			req.PageToken = proto.String(p.token)
		}
		if cmd.Flags().Changed("query") {
			req.Query = proto.String(query)
		}
		for _, value := range statuses {
			v, ok := corev1.GithubActionsJobStatus_value["GITHUB_ACTIONS_JOB_STATUS_"+strings.ToUpper(value)]
			if !ok || v == 0 {
				return fmt.Errorf("invalid --status %q: expected queued, waiting, running, or finished", value)
			}
			req.Statuses = append(req.Statuses, corev1.GithubActionsJobStatus(v))
		}
		for _, value := range conclusions {
			v, ok := corev1.GithubActionsJobConclusion_value["GITHUB_ACTIONS_JOB_CONCLUSION_"+strings.ToUpper(value)]
			if !ok || v == 0 {
				return fmt.Errorf("invalid --conclusion %q: expected success, failure, cancelled, skipped, or neutral", value)
			}
			req.Conclusions = append(req.Conclusions, corev1.GithubActionsJobConclusion(v))
		}
		if err := o.resolve(cmd); err != nil {
			return err
		}
		resp, err := api.ListGithubActionsJobs(cmd.Context(), o.token, o.org, req)
		if err != nil {
			return fmt.Errorf("failed to list GitHub Actions jobs: %w", err)
		}
		if o.output == "json" {
			return writeJSON(cmd.OutOrStdout(), resp)
		}
		return writeJobs(cmd.OutOrStdout(), resp)
	}
	o.flags(cmd)
	p.flags(cmd)
	f := cmd.Flags()
	f.StringArrayVar(&repos, "repo", nil, "Connected repository in owner/name format (repeatable)")
	f.StringArrayVar(&statuses, "status", nil, "Job state: queued, waiting, running, finished (repeatable)")
	f.StringArrayVar(&conclusions, "conclusion", nil, "Result: success, failure, cancelled, skipped, neutral (repeatable)")
	f.StringArrayVar(&labels, "runner-label", nil, "Runner label (repeatable)")
	f.StringVar(&query, "query", "", "Search job/workflow names or exact numeric job/run IDs")
	return cmd
}

func newCmdAnalytics() *cobra.Command {
	var o options
	var p pageOptions
	filters := &corev1.GithubActionsAnalyticsFilters{}
	cmd := &cobra.Command{
		Use:   "analytics",
		Short: "Show aggregate GitHub Actions job resource usage and timing",
		Args:  cobra.NoArgs,
		Long: `Show CPU and memory utilization, mean and p95 duration, and failure rate
by repository, workflow, and job. Results are ordered by descending job count.
The default window is the last 30 days; the maximum is 90 days.
Analytics may lag completed jobs by up to 15 minutes. Metrics are aggregated
across executions, not individual steps. JSON is automatic when stdout is piped.`,
		Example: `  depot jobs analytics --repo acme/api --job test --output json
  depot jobs analytics --start 2026-10-01T00:00:00Z --end 2026-10-08T00:00:00Z`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tr, err := p.timeRange()
			if err != nil {
				return err
			}
			if err := o.resolve(cmd); err != nil {
				return err
			}
			req := &corev1.ListGithubActionsJobAnalyticsRequest{TimeRange: tr, Filters: filters, PageSize: proto.Int32(p.size)}
			if p.token != "" {
				req.PageToken = proto.String(p.token)
			}
			resp, err := api.ListGithubActionsJobAnalytics(cmd.Context(), o.token, o.org, req)
			if err != nil {
				return fmt.Errorf("failed to list GitHub Actions job analytics: %w", err)
			}
			if o.output == "json" {
				return writeJSON(cmd.OutOrStdout(), resp)
			}
			return writeAnalytics(cmd.OutOrStdout(), resp)
		},
	}
	o.flags(cmd)
	p.flags(cmd)
	f := cmd.Flags()
	f.StringArrayVar(&filters.Repositories, "repo", nil, "Connected repository in owner/name format (repeatable)")
	f.StringArrayVar(&filters.Workflows, "workflow", nil, "Workflow display name (repeatable)")
	f.StringArrayVar(&filters.WorkflowPaths, "workflow-path", nil, "Workflow file path (repeatable)")
	f.StringArrayVar(&filters.Jobs, "job", nil, "Job key from the workflow file (repeatable)")
	f.StringArrayVar(&filters.RunnerLabels, "runner-label", nil, "Runner label (repeatable)")
	return cmd
}
