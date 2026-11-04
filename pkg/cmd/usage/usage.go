package usage

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	usagev1 "github.com/depot/cli/pkg/proto/depot/core/v1"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type options struct {
	org, token, start, end, output, product, group, pageToken, project string
	products, projects, repositories, paths, keys                      []string
	pageSize                                                           int32
}

// NewCmdUsage exposes recorded usage as protobuf JSON. It never prompts during automation.
func NewCmdUsage() *cobra.Command {
	cmd := &cobra.Command{Use: "usage", Short: "Analyze organization usage", Long: "Read recorded usage and UTC daily series. Values are usage quantities, not invoice amounts. All results use JSON; follow nextPageToken with the same time range and filters.", Args: cobra.NoArgs}
	for _, kind := range []string{"series", "breakdown", "report", "current", "agents"} {
		cmd.AddCommand(newCommand(kind))
	}
	return cmd
}

func newCommand(kind string) *cobra.Command {
	opts := options{}
	descriptions := map[string]string{"series": "Read daily usage and period totals", "breakdown": "List usage by project, repository, workflow, job, or runner", "report": "Read structured usage export rows", "current": "Read current billing-period usage", "agents": "Read agent sandbox usage"}
	cmd := &cobra.Command{
		Use: kind, Short: descriptions[kind], Args: cobra.NoArgs,
		Example: "  # Read daily usage for an explicit UTC period\n  depot usage series --start 2026-10-01T00:00:00Z --end 2026-10-08T00:00:00Z --output json\n\n  # Attribute Depot CI usage to workflows\n  depot usage breakdown --product depot-ci --group-by workflow --repo owner/repo --output json",
		RunE:    func(cmd *cobra.Command, _ []string) error { return run(cmd, kind, opts) },
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.org, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	flags.StringVar(&opts.token, "token", "", "Depot API token")
	flags.StringVarP(&opts.output, "output", "o", "json", "Output format (json)")
	if kind != "current" {
		flags.StringVar(&opts.start, "start", "", "Inclusive start in RFC 3339 format (default: start of current UTC month)")
		flags.StringVar(&opts.end, "end", "", "Exclusive end in RFC 3339 format (default: now)")
		cmd.MarkFlagsRequiredTogether("start", "end")
	}
	if kind == "series" {
		flags.StringArrayVar(&opts.products, "product", nil, "Product to include (container-builds, github-actions, depot-ci, test-results, storage, agent-sandboxes); repeatable")
		flags.StringVar(&opts.project, "project", "", "Project ID for container-build and layer-cache daily usage")
		cmd.MarkFlagsMutuallyExclusive("product", "project")
	}
	if kind == "breakdown" {
		flags.StringVar(&opts.product, "product", "", "Product (container-builds, github-actions, depot-ci, test-results)")
		flags.StringVar(&opts.group, "group-by", "", "Group (project, repository, runner, job, workflow)")
		flags.StringArrayVar(&opts.projects, "project", nil, "Project ID to include (container-builds only); repeatable")
		flags.StringArrayVar(&opts.repositories, "repo", nil, "Repository in owner/name format; repeatable")
		flags.StringArrayVar(&opts.paths, "workflow-path", nil, "GitHub Actions workflow path (job groups only); repeatable; empty selects unattributed usage")
		flags.StringArrayVar(&opts.keys, "workflow-key", nil, "Depot CI stable workflow key (workflow groups only); repeatable")
		flags.Int32Var(&opts.pageSize, "page-size", 100, "Number of groups per page (max 500)")
		flags.StringVar(&opts.pageToken, "page-token", "", "Token to fetch the next page with unchanged filters")
		_ = cmd.MarkFlagRequired("product")
		_ = cmd.MarkFlagRequired("group-by")
	}
	return cmd
}

func run(cmd *cobra.Command, kind string, opts options) error {
	if opts.output != "json" {
		return fmt.Errorf("--output must be json")
	}
	period, err := parsePeriod(opts.start, opts.end)
	if err != nil {
		return err
	}
	if err := validate(kind, opts); err != nil {
		return err
	}
	token := helpers.ResolveStaticOrgAuth(opts.token)
	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login` or set DEPOT_TOKEN")
	}
	if opts.org == "" {
		opts.org = config.GetCurrentOrganization()
	}
	client := api.NewUsageAnalyticsClient()
	ctx := cmd.Context()
	var response proto.Message
	switch kind {
	case "series":
		if opts.project != "" {
			response, err = call(ctx, token, opts.org, &usagev1.GetProjectUsageSeriesRequest{ProjectId: opts.project, TimeRange: period}, client.GetProjectUsageSeries)
		} else {
			response, err = call(ctx, token, opts.org, &usagev1.GetOrganizationUsageSeriesRequest{TimeRange: period, Products: opts.products}, client.GetOrganizationUsageSeries)
		}
	case "report":
		response, err = call(ctx, token, opts.org, &usagev1.GetUsageReportRequest{TimeRange: period}, client.GetUsageReport)
	case "current":
		response, err = call(ctx, token, opts.org, &usagev1.GetCurrentPeriodUsageRequest{}, client.GetCurrentPeriodUsage)
	case "agents":
		response, err = call(ctx, token, opts.org, &usagev1.GetAgentSandboxUsageRequest{TimeRange: period}, client.GetAgentSandboxUsage)
	case "breakdown":
		switch opts.product + "/" + opts.group {
		case "container-builds/project":
			response, err = call(ctx, token, opts.org, &usagev1.ListContainerBuildUsageByProjectRequest{TimeRange: period, PageSize: opts.pageSize, PageToken: opts.pageToken, ProjectIds: opts.projects}, client.ListContainerBuildUsageByProject)
		case "github-actions/repository":
			response, err = call(ctx, token, opts.org, &usagev1.ListGithubActionsUsageByRepositoryRequest{TimeRange: period, PageSize: opts.pageSize, PageToken: opts.pageToken, Repositories: opts.repositories}, client.ListGithubActionsUsageByRepository)
		case "github-actions/runner":
			response, err = call(ctx, token, opts.org, &usagev1.ListGithubActionsUsageByRunnerRequest{TimeRange: period, PageSize: opts.pageSize, PageToken: opts.pageToken, Repositories: opts.repositories}, client.ListGithubActionsUsageByRunner)
		case "github-actions/job":
			response, err = call(ctx, token, opts.org, &usagev1.ListGithubActionsUsageByJobRequest{TimeRange: period, PageSize: opts.pageSize, PageToken: opts.pageToken, Repositories: opts.repositories, WorkflowPaths: opts.paths}, client.ListGithubActionsUsageByJob)
		case "depot-ci/repository":
			response, err = call(ctx, token, opts.org, &usagev1.ListDepotCIUsageByRepositoryRequest{TimeRange: period, PageSize: opts.pageSize, PageToken: opts.pageToken, Repositories: opts.repositories}, client.ListDepotCIUsageByRepository)
		case "depot-ci/workflow":
			response, err = call(ctx, token, opts.org, &usagev1.ListDepotCIUsageByWorkflowRequest{TimeRange: period, PageSize: opts.pageSize, PageToken: opts.pageToken, Repositories: opts.repositories, WorkflowKeys: opts.keys}, client.ListDepotCIUsageByWorkflow)
		case "test-results/repository":
			response, err = call(ctx, token, opts.org, &usagev1.ListTestResultsUsageByRepositoryRequest{TimeRange: period, PageSize: opts.pageSize, PageToken: opts.pageToken, Repositories: opts.repositories}, client.ListTestResultsUsageByRepository)
		}
	}
	if err != nil {
		return fmt.Errorf("read usage: %w", err)
	}
	data, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(response)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}

func call[Request any, Response any](ctx context.Context, token, org string, request *Request, method func(context.Context, *connect.Request[Request]) (*connect.Response[Response], error)) (*Response, error) {
	response, err := method(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(request), token, org))
	if err != nil {
		return nil, err
	}
	return response.Msg, nil
}

func parsePeriod(start, end string) (*usagev1.UsageAnalyticsTimeRange, error) {
	if start == "" && end == "" {
		return nil, nil
	}
	a, err := time.Parse(time.RFC3339Nano, start)
	if err != nil {
		return nil, fmt.Errorf("--start must be an RFC 3339 timestamp: %w", err)
	}
	b, err := time.Parse(time.RFC3339Nano, end)
	if err != nil {
		return nil, fmt.Errorf("--end must be an RFC 3339 timestamp: %w", err)
	}
	if !b.After(a) || b.Sub(a) > 93*24*time.Hour {
		return nil, fmt.Errorf("--end must be after --start, within 93 days")
	}
	return &usagev1.UsageAnalyticsTimeRange{StartAt: timestamppb.New(a), EndAt: timestamppb.New(b)}, nil
}

func validate(kind string, opts options) error {
	if kind == "series" {
		for _, product := range opts.products {
			switch product {
			case "container-builds", "github-actions", "depot-ci", "test-results", "storage", "agent-sandboxes":
			default:
				return fmt.Errorf("unknown --product %q", product)
			}
		}
	}
	if kind != "breakdown" {
		return nil
	}
	if opts.pageSize < 1 || opts.pageSize > 500 {
		return fmt.Errorf("--page-size must be between 1 and 500")
	}
	switch opts.product + "/" + opts.group {
	case "container-builds/project", "github-actions/repository", "github-actions/runner", "github-actions/job", "depot-ci/repository", "depot-ci/workflow", "test-results/repository":
	default:
		return fmt.Errorf("unsupported --product and --group-by combination")
	}
	if len(opts.projects) > 0 && opts.product != "container-builds" {
		return fmt.Errorf("--project requires --product container-builds")
	}
	if len(opts.repositories) > 0 && opts.product == "container-builds" {
		return fmt.Errorf("--repo is unavailable for container-builds")
	}
	if len(opts.paths) > 0 && (opts.product != "github-actions" || opts.group != "job") {
		return fmt.Errorf("--workflow-path requires --product github-actions --group-by job")
	}
	if len(opts.keys) > 0 && (opts.product != "depot-ci" || opts.group != "workflow") {
		return fmt.Errorf("--workflow-key requires --product depot-ci --group-by workflow")
	}
	return nil
}
