package builds

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type options struct {
	project, token, output, start, end string
}

func NewCmdBuilds() *cobra.Command {
	cmd := &cobra.Command{
		Use: "builds", Short: "Inspect container-build resource utilization", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newCmdMetrics(), newCmdAnalytics())
	return cmd
}

const coverageHelp = `Measurements cover build-owned executor work only, including user and kernel CPU.
Cache hits, idle gaps, daemon overhead, and shared or unsupported work are not
measured. Older builders have no observations. Missing measurements remain
absent in JSON, blank in CSV, and n/a in tables; they are never zero-filled.
Averages are weighted by observed duration. Memory averages use each interval's
highest sampled executor-memory sum. Memory peaks are sampled, and
summary peaks are individual build-machine peaks, not simultaneous totals.

Requires an organization API token via --token or DEPOT_TOKEN; its organization
determines access. User-login and project tokens are not supported.
Use --project, DEPOT_PROJECT_ID, or a local depot.json to select the project.
Both --start and --end are required. They select completed builds, inclusive at
the start and exclusive at the end, with a maximum range of seven days.
JSON is automatic when stdout is piped. JSON and CSV utilization is a fraction
of observed builder capacity; tables show percentages. Memory sizes use bytes.`

func (o *options) flags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&o.project, "project", "", "Depot project ID (or DEPOT_PROJECT_ID or depot.json)")
	f.StringVar(&o.token, "token", "", "Organization API token (or DEPOT_TOKEN)")
	f.StringVarP(&o.output, "output", "o", "auto", "Output format (auto, table, json, csv)")
	f.StringVar(&o.start, "start", "", "Inclusive completion time (RFC 3339, millisecond precision; required)")
	f.StringVar(&o.end, "end", "", "Exclusive completion time (RFC 3339, millisecond precision; required)")
}

func (o *options) resolve() (*timestamppb.Timestamp, *timestamppb.Timestamp, error) {
	switch o.output {
	case "auto":
		o.output = "json"
		if helpers.IsTerminal() {
			o.output = "table"
		}
	case "table", "json", "csv":
	default:
		return nil, nil, fmt.Errorf("--output must be auto, table, json, or csv")
	}
	if o.start == "" || o.end == "" {
		return nil, nil, fmt.Errorf("--start and --end are required")
	}
	start, err := parseTime(o.start, "start")
	if err != nil {
		return nil, nil, err
	}
	end, err := parseTime(o.end, "end")
	if err != nil {
		return nil, nil, err
	}
	if !end.After(start) || end.Sub(start) > 7*24*time.Hour {
		return nil, nil, fmt.Errorf("time range must be positive and at most seven days")
	}
	if end.After(time.Now()) {
		return nil, nil, fmt.Errorf("--end must not be in the future")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	o.project = helpers.ResolveProjectID(o.project, cwd)
	if strings.TrimSpace(o.project) == "" {
		return nil, nil, fmt.Errorf("a project is required (--project, DEPOT_PROJECT_ID, or depot.json)")
	}
	if o.token == "" {
		o.token = os.Getenv("DEPOT_TOKEN")
	}
	if strings.TrimSpace(o.token) == "" {
		return nil, nil, fmt.Errorf("an organization API token is required (--token or DEPOT_TOKEN)")
	}
	return timestamppb.New(start), timestamppb.New(end), nil
}

func parseTime(value, flag string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || timestamppb.New(t).CheckValid() != nil || t.Unix() < 0 || t.Nanosecond()%1_000_000 != 0 {
		return time.Time{}, fmt.Errorf("--%s must be an RFC 3339 timestamp on or after 1970-01-01 with at most millisecond precision", flag)
	}
	return t, nil
}

func newCmdMetrics() *cobra.Command {
	var o options
	var pageSize int32
	var pageToken string
	cmd := &cobra.Command{
		Use: "metrics", Short: "Export container-build CPU and memory measurements", Args: cobra.NoArgs,
		Long: `Return one page of completed builds, newest first, with per-machine statistics.
JSON preserves the response and nextPageToken. CSV has one row per build/machine;
builds without machines still get a row. Table and CSV continuation tokens are
printed to stderr. Keep the project and dates unchanged when requesting another page.

` + coverageHelp,
		Example: `  # Inspect one page of build measurements
  depot builds metrics --project <project-id> --start 2026-11-01T00:00:00Z --end 2026-11-02T00:00:00Z --output table

  # Export a page for analysis
  depot builds metrics --project <project-id> --start 2026-11-01T00:00:00Z --end 2026-11-02T00:00:00Z --output csv`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if pageSize < 1 || pageSize > 50 {
				return fmt.Errorf("--page-size must be between 1 and 50")
			}
			start, end, err := o.resolve()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			resp, err := api.NewCoreBuildClient().ListBuildMetrics(ctx, api.WithAuthentication(connect.NewRequest(&corev1.ListBuildMetricsRequest{
				ProjectId: o.project, StartAt: start, EndAt: end, PageSize: pageSize, PageToken: pageToken,
			}), o.token))
			if err != nil {
				return requestError(err)
			}
			if o.output == "json" {
				return writeJSON(cmd.OutOrStdout(), resp.Msg)
			}
			if err := writeMetrics(cmd.OutOrStdout(), o, resp.Msg); err != nil {
				return err
			}
			if resp.Msg.NextPageToken != "" {
				_, err = fmt.Fprintf(cmd.ErrOrStderr(), "Next page token: %s\nUse --page-token with the same --project, --start, and --end.\n", cell(resp.Msg.NextPageToken))
			}
			return err
		},
	}
	o.flags(cmd)
	cmd.Flags().Int32Var(&pageSize, "page-size", 20, "Builds per page (1-50)")
	cmd.Flags().StringVar(&pageToken, "page-token", "", "Next page token; keep project and dates unchanged")
	return cmd
}

func newCmdAnalytics() *cobra.Command {
	var o options
	cmd := &cobra.Command{
		Use: "analytics", Short: "Show aggregate container-build CPU and memory measurements", Args: cobra.NoArgs,
		Long: `Return project and architecture summaries for at most 1,000 completed builds.
Narrow the date range if the server reports a resource limit. All aggregates
come from the API; the CLI never averages a page of build results.

` + coverageHelp,
		Example: `  # Inspect aggregate utilization
  depot builds analytics --project <project-id> --start 2026-11-01T00:00:00Z --end 2026-11-02T00:00:00Z --output table

  # Export the complete project and architecture summaries
  depot builds analytics --project <project-id> --start 2026-11-01T00:00:00Z --end 2026-11-02T00:00:00Z --output json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			start, end, err := o.resolve()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			resp, err := api.NewCoreBuildClient().GetProjectBuildMetrics(ctx, api.WithAuthentication(connect.NewRequest(&corev1.GetProjectBuildMetricsRequest{
				ProjectId: o.project, StartAt: start, EndAt: end,
			}), o.token))
			if err != nil {
				return requestError(err)
			}
			if o.output == "json" {
				return writeJSON(cmd.OutOrStdout(), resp.Msg)
			}
			return writeAnalytics(cmd.OutOrStdout(), o, resp.Msg)
		},
	}
	o.flags(cmd)
	return cmd
}

func requestError(err error) error {
	code := connect.CodeOf(err)
	if code == connect.CodeResourceExhausted {
		return fmt.Errorf("build metrics request exceeded server limits; narrow --start and --end or reduce --page-size for metrics (resource_exhausted)")
	}
	if code == connect.CodeUnimplemented {
		return fmt.Errorf("the API does not support build metrics yet (unimplemented)")
	}
	return fmt.Errorf("build metrics request failed (%s)", code)
}
