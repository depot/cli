package tests

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/depot/cli/pkg/api"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

var listTestAnalyticsFunc = api.ListTestAnalytics

func newCmdTestsAnalytics() *cobra.Command {
	var orgID, token, output, ranking string
	var ci, gha bool
	req := &testresultsv1.ListTestAnalyticsRequest{}
	cmd := &cobra.Command{
		Use:   "analytics",
		Short: "Find flaky or slow tests across runs",
		Long: `Rank tests across Depot CI and GitHub Actions executions.
Flaky tests have passing and failing executions for the same repository, ref,
and commit. Slow tests rank by p95 duration. Results include recent failure
context and owner IDs for drill-down with depot tests <owner-id>.

Dates are inclusive UTC calendar dates; the default window is the last seven
days including today. The maximum window is 90 days. JSON output is automatic
when stdout is piped and never prompts for authentication.`,
		Example: `  depot tests analytics --ranking flaky --repo acme/api
  depot tests analytics --ranking slowest --output json
  depot tests analytics --start-date 2026-09-01 --end-date 2026-09-07 --ci`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if ci && gha {
				return fmt.Errorf("--ci and --gha are mutually exclusive")
			}
			switch ranking {
			case "flaky":
				req.Ranking = testresultsv1.TestAnalyticsRanking_TEST_ANALYTICS_RANKING_FLAKY
			case "slowest":
				req.Ranking = testresultsv1.TestAnalyticsRanking_TEST_ANALYTICS_RANKING_SLOWEST
			default:
				return fmt.Errorf("--ranking must be flaky or slowest")
			}
			if req.Limit > 100 {
				return fmt.Errorf("--limit must be <= 100")
			}
			switch output {
			case "auto", "table", "json":
			default:
				return fmt.Errorf("--output must be auto, table, or json")
			}
			if err := validateAnalyticsDates(req.StartDate, req.EndDate); err != nil {
				return err
			}
			if ci {
				req.OwnerType = testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_CI
			}
			if gha {
				req.OwnerType = testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_GITHUB_ACTIONS
			}
			if orgID == "" {
				orgID = currentOrgFunc()
			}
			format := outputFormat(output)
			resolvedToken, err := resolveToken(cmd.Context(), token, format)
			if err != nil {
				return err
			}
			if resolvedToken == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}
			resp, err := listTestAnalyticsFunc(cmd.Context(), resolvedToken, orgID, req)
			if err != nil {
				return fmt.Errorf("failed to list test analytics: %w", err)
			}
			if format == "json" {
				data, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(resp)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", data)
				return err
			}
			return writeAnalyticsTable(cmd.OutOrStdout(), resp)
		},
	}
	f := cmd.Flags()
	f.StringVar(&orgID, "org", "", "Organization ID")
	f.StringVar(&token, "token", "", "Depot API token")
	f.StringVar(&output, "output", "auto", "Output format (auto, table, json)")
	f.StringVar(&ranking, "ranking", "flaky", "Rank flaky or slowest tests")
	f.StringVar(&req.StartDate, "start-date", "", "First UTC date to include (YYYY-MM-DD)")
	f.StringVar(&req.EndDate, "end-date", "", "Last UTC date to include (YYYY-MM-DD)")
	f.BoolVar(&ci, "ci", false, "Only Depot CI results")
	f.BoolVar(&gha, "gha", false, "Only GitHub Actions results")
	f.StringVar(&req.Repo, "repo", "", "Exact repository identity")
	f.StringVar(&req.Ref, "ref", "", "Exact source ref (for example refs/heads/main)")
	f.StringVar(&req.SuiteName, "suite", "", "Exact representative test suite name")
	f.StringVar(&req.TestName, "test", "", "Case-insensitive test name substring")
	f.StringVar(&req.ClassName, "class", "", "Exact representative class name")
	f.StringVar(&req.FileName, "file", "", "Exact representative filename")
	f.Uint32Var(&req.Limit, "limit", 20, "Maximum ranked tests to return (max 100)")
	return cmd
}

func validateAnalyticsDates(start, end string) error {
	endTime := time.Now().UTC().Truncate(24 * time.Hour)
	var err error
	if end != "" {
		endTime, err = time.Parse("2006-01-02", end)
		if err != nil {
			return fmt.Errorf("--end-date must be a valid YYYY-MM-DD date")
		}
	}
	if start == "" {
		return nil
	}
	startTime, err := time.Parse("2006-01-02", start)
	if err != nil {
		return fmt.Errorf("--start-date must be a valid YYYY-MM-DD date")
	}
	if endTime.Before(startTime) || endTime.Sub(startTime) >= 90*24*time.Hour {
		return fmt.Errorf("date range must be between 1 and 90 calendar days")
	}
	return nil
}

func writeAnalyticsTable(w io.Writer, resp *testresultsv1.ListTestAnalyticsResponse) error {
	if len(resp.Tests) == 0 {
		_, err := fmt.Fprintln(w, "No matching test analytics found.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SOURCE\tREPOSITORY\tTEST\tRESULTS\tFAILED\tERRORED\tFLAKE RATE\tP95\tFAILURE OWNER")
	for _, row := range resp.Tests {
		source := "ci"
		if row.OwnerType == testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_GITHUB_ACTIONS {
			source = "gha"
		}
		flake, duration := "-", "-"
		if row.FlakeRate != nil {
			flake = fmt.Sprintf("%.1f%%", row.GetFlakeRate()*100)
		}
		if row.P95DurationMs != nil {
			duration = fmt.Sprintf("%.1fms", row.GetP95DurationMs())
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\n", source,
			sanitizeCell(row.Repo), sanitizeCell(row.TestName), row.Total, row.Failed, row.Errored,
			flake, duration, sanitizeCell(row.GetLatestFailure().GetOwnerId()))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if resp.HasMore {
		_, err := fmt.Fprintln(w, "More tests match. Increase --limit or narrow the filters.")
		return err
	}
	return nil
}
