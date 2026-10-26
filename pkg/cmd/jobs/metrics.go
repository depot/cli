package jobs

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"github.com/depot/cli/pkg/api"
	"github.com/spf13/cobra"
)

func newCmdMetrics() *cobra.Command {
	var o options
	var repos []string
	cmd := &cobra.Command{
		Use:   "metrics <job-id>",
		Short: "Fetch GitHub Actions job CPU, memory, and OOM events",
		Args:  cobra.ExactArgs(1),
		Long: `Fetch CPU and memory utilization for one GitHub Actions job execution.
Requires a GitHub job ID and exactly one --repo owner/name.
Supports completed jobs and snapshots of running jobs. Missing measurements
remain absent in JSON and appear as n/a in table summaries.
JSON includes time-ordered samples, statistics, availability, and sample-cap
metadata. Utilization is a fraction from 0 to 1 in JSON and a percentage in
tables. CPU measures user-mode utilization averaged across cores; memory is
used memory as a fraction of total memory.
OOM evidence includes kernel kills observed on the runner during this job.
No observed events does not rule out OOM; events may arrive after completion.
At most 100 OOM events are returned, with truncation indicated in JSON.
The API limits execution windows to seven days and returns at most 5,000 samples.
Statistics cover all observed samples before downsampling; returned samples
may omit intermediate peaks. JSON is automatic when stdout is piped and does
not prompt for login.`,
		Example: `  depot jobs metrics 123456789 --repo acme/api
  depot jobs metrics 123456789 --repo acme/api --org org_123 --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("a non-empty GitHub job ID is required")
			}
			if len(repos) != 1 || strings.TrimSpace(repos[0]) == "" {
				return fmt.Errorf("a job ID requires exactly one --repo owner/name")
			}
			if err := o.resolve(cmd); err != nil {
				return err
			}
			resp, err := api.GetGithubActionsJobMetrics(cmd.Context(), o.token, o.org, &corev1.GetGithubActionsJobMetricsRequest{
				Repository: repos[0], JobId: args[0],
			})
			if err != nil {
				return fmt.Errorf("failed to get GitHub Actions job metrics: %w", err)
			}
			if resp.Job == nil {
				return fmt.Errorf("GitHub Actions job metrics response contained no job")
			}
			if o.output == "json" {
				return writeJSON(cmd.OutOrStdout(), resp)
			}
			return writeMetrics(cmd.OutOrStdout(), resp)
		},
	}
	o.flags(cmd)
	cmd.Flags().StringArrayVar(&repos, "repo", nil, "Connected repository in owner/name format (exactly one required)")
	return cmd
}

func writeMetrics(w io.Writer, resp *corev1.GetGithubActionsJobMetricsResponse) error {
	if err := writeJob(w, resp.Job); err != nil {
		return err
	}
	stats := resp.GetStats()
	if stats == nil {
		stats = &corev1.GithubActionsMetricsStats{}
	}
	availability := resp.GetAvailability()
	cap := resp.GetCap()
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"Snapshot", timestamp(resp.GetSnapshotAt())},
		{"Availability", strings.ToLower(strings.TrimPrefix(availability.GetCode().String(), "GITHUB_ACTIONS_METRICS_AVAILABILITY_CODE_"))},
		{"Reason", availability.GetReason()},
		{"Observed start", timestamp(stats.GetObservedStartedAt())},
		{"Observed finish", timestamp(stats.GetObservedFinishedAt())},
		{"CPU (user mode)", fmt.Sprintf("avg %s, peak %s (%d samples)", metricPercent(stats.AverageCpuUtilization), metricPercent(stats.PeakCpuUtilization), stats.GetCpuSampleCount())},
		{"Memory", fmt.Sprintf("avg %s, peak %s (%d samples)", metricPercent(stats.AverageMemoryUtilization), metricPercent(stats.PeakMemoryUtilization), stats.GetMemorySampleCount())},
		{"Samples", fmt.Sprintf("%d returned / %d raw (limit %d)", cap.GetReturnedSampleCount(), cap.GetRawSampleCount(), cap.GetMaxReturnedSampleCount())},
		{"Downsampled", fmt.Sprint(cap.GetDownsampled())},
	} {
		fmt.Fprintf(tw, "%s:\t%s\n", row[0], cell(row[1]))
	}
	if cap.GetDownsampled() {
		fmt.Fprintf(tw, "Downsampling strategy:\t%s\n", cell(cap.GetDownsampleStrategy()))
		fmt.Fprintln(tw, "Returned samples may omit intermediate peaks; statistics use all observed samples.")
	}
	fmt.Fprintln(tw, "Full samples: use --output json.")
	if err := tw.Flush(); err != nil {
		return err
	}
	return writeOOM(w, resp.GetOom())
}

func metricPercent(value *float64) string {
	if value == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", *value*100)
}
