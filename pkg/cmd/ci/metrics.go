package ci

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

var (
	ciGetJobAttemptMetrics = api.CIGetJobAttemptMetrics
	ciGetJobMetrics        = api.CIGetJobMetrics
	ciGetRunMetrics        = api.CIGetRunMetrics
)

func NewCmdMetrics() *cobra.Command {
	var (
		orgID     string
		token     string
		output    string
		attemptID string
		jobID     string
		runID     string
	)

	cmd := &cobra.Command{
		Use:   "metrics <attempt-id>",
		Short: "Fetch CI CPU and memory metrics",
		Long:  "Fetch CPU and memory metrics for a CI job attempt, job, or run.",
		Example: `  depot ci metrics att_123
  depot ci metrics --attempt att_123 --output json
  depot ci metrics --job job_123 --output json
  depot ci metrics --run run_123 --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateMetricsOutput(output); err != nil {
				return err
			}
			if len(args) > 1 {
				return fmt.Errorf("expected at most one attempt ID")
			}
			if len(args) == 1 && (attemptID != "" || jobID != "" || runID != "") {
				return fmt.Errorf("positional attempt ID cannot be combined with --attempt, --job, or --run")
			}
			if countNonEmpty(attemptID, jobID, runID) > 1 {
				return fmt.Errorf("--attempt, --job, and --run are mutually exclusive")
			}
			if len(args) == 0 && countNonEmpty(attemptID, jobID, runID) == 0 {
				return cmd.Help()
			}
			if len(args) == 1 {
				attemptID = args[0]
			}

			ctx := cmd.Context()
			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			orgFlag := ""
			if cmd.Flags().Changed("org") {
				orgFlag = " --org " + orgID
			}

			switch {
			case jobID != "":
				resp, err := ciGetJobMetrics(ctx, tokenVal, orgID, jobID)
				if err != nil {
					if connect.CodeOf(err) == connect.CodeResourceExhausted {
						return connectErrorMessage(err)
					}
					return fmt.Errorf("failed to get job metrics: %w", err)
				}
				if metricsOutputJSON(output) {
					return writeJSON(buildJobMetricsJSON(resp))
				}
				printJobMetrics(resp, orgFlag)
			case runID != "":
				resp, err := ciGetRunMetrics(ctx, tokenVal, orgID, runID)
				if err != nil {
					if connect.CodeOf(err) == connect.CodeResourceExhausted {
						return connectErrorMessage(err)
					}
					return fmt.Errorf("failed to get run metrics: %w", err)
				}
				if metricsOutputJSON(output) {
					return writeJSON(buildRunMetricsJSON(resp))
				}
				printRunMetrics(resp, orgFlag)
			default:
				resp, err := ciGetJobAttemptMetrics(ctx, tokenVal, orgID, attemptID)
				if err != nil {
					if connect.CodeOf(err) == connect.CodeNotFound {
						return fmt.Errorf("attempt not found; use --job <job-id> or --run <run-id> for job/run metrics: %w", err)
					}
					return fmt.Errorf("failed to get attempt metrics: %w", err)
				}
				if metricsOutputJSON(output) {
					return writeJSON(buildAttemptMetricsJSON(resp))
				}
				printAttemptMetrics(resp, orgFlag)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (text, json)")
	cmd.Flags().StringVar(&attemptID, "attempt", "", "Job attempt ID")
	cmd.Flags().StringVar(&jobID, "job", "", "Job ID")
	cmd.Flags().StringVar(&runID, "run", "", "Run ID")

	return cmd
}

type attemptMetricsDocument struct {
	Type       string                         `json:"type"`
	SnapshotAt string                         `json:"snapshot_at"`
	Run        *civ1.CIMetricsRunContext      `json:"run"`
	Workflow   *civ1.CIMetricsWorkflowContext `json:"workflow"`
	Job        *civ1.CIMetricsJobContext      `json:"job"`
	Attempt    attemptMetricsJSON             `json:"attempt"`
}

type jobMetricsDocument struct {
	Type       string                         `json:"type"`
	SnapshotAt string                         `json:"snapshot_at"`
	Run        *civ1.CIMetricsRunContext      `json:"run"`
	Workflow   *civ1.CIMetricsWorkflowContext `json:"workflow"`
	Job        *civ1.CIMetricsJobContext      `json:"job"`
	Attempts   []attemptSummaryJSON           `json:"attempts"`
}

type runMetricsDocument struct {
	Type       string                    `json:"type"`
	SnapshotAt string                    `json:"snapshot_at"`
	Run        *civ1.CIMetricsRunContext `json:"run"`
	Workflows  []workflowMetricsJSON     `json:"workflows"`
}

type workflowMetricsJSON struct {
	Workflow *civ1.CIMetricsWorkflowContext `json:"workflow"`
	Jobs     []jobMetricsJSON               `json:"jobs"`
}

type jobMetricsJSON struct {
	Job      *civ1.CIMetricsJobContext `json:"job"`
	Attempts []attemptSummaryJSON      `json:"attempts"`
}

type attemptMetricsJSON struct {
	Attempt      *civ1.CIMetricsAttemptContext `json:"attempt"`
	Availability availabilityJSON              `json:"availability"`
	Stats        statsJSON                     `json:"stats"`
	Cap          capJSON                       `json:"cap"`
	Samples      []sampleJSON                  `json:"samples"`
}

type attemptSummaryJSON struct {
	Attempt      *civ1.CIMetricsAttemptContext `json:"attempt"`
	Availability availabilityJSON              `json:"availability"`
	Stats        statsJSON                     `json:"stats"`
	Cap          capJSON                       `json:"cap"`
}

type availabilityJSON struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

type statsJSON struct {
	SampleCount              uint32   `json:"sample_count"`
	CpuSampleCount           uint32   `json:"cpu_sample_count"`
	MemorySampleCount        uint32   `json:"memory_sample_count"`
	PeakCpuUtilization       *float64 `json:"peak_cpu_utilization,omitempty"`
	AverageCpuUtilization    *float64 `json:"average_cpu_utilization,omitempty"`
	PeakMemoryUtilization    *float64 `json:"peak_memory_utilization,omitempty"`
	AverageMemoryUtilization *float64 `json:"average_memory_utilization,omitempty"`
	ObservedStartedAt        string   `json:"observed_started_at"`
	ObservedFinishedAt       string   `json:"observed_finished_at"`
	PeakMemoryUsageBytes     *uint64  `json:"peak_memory_usage_bytes,omitempty"`
	AverageMemoryUsageBytes  *float64 `json:"average_memory_usage_bytes,omitempty"`
}

type capJSON struct {
	RawSampleCount         uint32 `json:"raw_sample_count"`
	ReturnedSampleCount    uint32 `json:"returned_sample_count"`
	MaxReturnedSampleCount uint32 `json:"max_returned_sample_count"`
	Downsampled            bool   `json:"downsampled"`
	DownsampleStrategy     string `json:"downsample_strategy"`
}

type sampleJSON struct {
	Timestamp         string   `json:"timestamp"`
	CpuUtilization    *float64 `json:"cpu_utilization,omitempty"`
	MemoryUtilization *float64 `json:"memory_utilization,omitempty"`
	MemoryUsageBytes  *uint64  `json:"memory_usage_bytes,omitempty"`
}

func buildAttemptMetricsJSON(resp *civ1.GetJobAttemptMetricsResponse) attemptMetricsDocument {
	return attemptMetricsDocument{
		Type:       "attempt",
		SnapshotAt: resp.GetSnapshotAt(),
		Run:        resp.GetRun(),
		Workflow:   resp.GetWorkflow(),
		Job:        resp.GetJob(),
		Attempt:    metricAttemptJSON(resp.GetAttempt()),
	}
}

func buildJobMetricsJSON(resp *civ1.GetJobMetricsResponse) jobMetricsDocument {
	attempts := make([]attemptSummaryJSON, 0, len(resp.GetAttempts()))
	for _, attempt := range resp.GetAttempts() {
		attempts = append(attempts, metricAttemptSummaryJSON(attempt))
	}
	return jobMetricsDocument{
		Type:       "job",
		SnapshotAt: resp.GetSnapshotAt(),
		Run:        resp.GetRun(),
		Workflow:   resp.GetWorkflow(),
		Job:        resp.GetJob(),
		Attempts:   attempts,
	}
}

func buildRunMetricsJSON(resp *civ1.GetRunMetricsResponse) runMetricsDocument {
	workflows := make([]workflowMetricsJSON, 0, len(resp.GetWorkflows()))
	for _, workflow := range resp.GetWorkflows() {
		jobs := make([]jobMetricsJSON, 0, len(workflow.GetJobs()))
		for _, job := range workflow.GetJobs() {
			attempts := make([]attemptSummaryJSON, 0, len(job.GetAttempts()))
			for _, attempt := range job.GetAttempts() {
				attempts = append(attempts, metricAttemptSummaryJSON(attempt))
			}
			jobs = append(jobs, jobMetricsJSON{Job: job.GetJob(), Attempts: attempts})
		}
		workflows = append(workflows, workflowMetricsJSON{Workflow: workflow.GetWorkflow(), Jobs: jobs})
	}
	return runMetricsDocument{
		Type:       "run",
		SnapshotAt: resp.GetSnapshotAt(),
		Run:        resp.GetRun(),
		Workflows:  workflows,
	}
}

func metricAttemptJSON(attempt *civ1.CIMetricsAttemptMetrics) attemptMetricsJSON {
	samples := make([]sampleJSON, 0, len(attempt.GetSamples()))
	for _, sample := range attempt.GetSamples() {
		samples = append(samples, sampleJSON{
			Timestamp:         sample.GetTimestamp(),
			CpuUtilization:    sample.CpuUtilization,
			MemoryUtilization: sample.MemoryUtilization,
			MemoryUsageBytes:  sample.MemoryUsageBytes,
		})
	}
	return attemptMetricsJSON{
		Attempt:      attempt.GetAttempt(),
		Availability: metricAvailabilityJSON(attempt.GetAvailability()),
		Stats:        metricStatsJSON(attempt.GetStats()),
		Cap:          metricCapJSON(attempt.GetCap()),
		Samples:      samples,
	}
}

func metricAttemptSummaryJSON(attempt *civ1.CIMetricsAttemptSummary) attemptSummaryJSON {
	return attemptSummaryJSON{
		Attempt:      attempt.GetAttempt(),
		Availability: metricAvailabilityJSON(attempt.GetAvailability()),
		Stats:        metricStatsJSON(attempt.GetStats()),
		Cap:          metricCapJSON(attempt.GetCap()),
	}
}

func metricAvailabilityJSON(availability *civ1.CIMetricsAvailability) availabilityJSON {
	return availabilityJSON{
		Code:   metricAvailabilityCode(availability.GetCode()),
		Reason: availability.GetReason(),
	}
}

func metricAvailabilityCode(code civ1.CIMetricsAvailabilityCode) string {
	switch code {
	case civ1.CIMetricsAvailabilityCode_CI_METRICS_AVAILABILITY_CODE_AVAILABLE:
		return "available"
	case civ1.CIMetricsAvailabilityCode_CI_METRICS_AVAILABILITY_CODE_NO_SANDBOX:
		return "no_sandbox"
	case civ1.CIMetricsAvailabilityCode_CI_METRICS_AVAILABILITY_CODE_NO_TIME_RANGE:
		return "no_time_range"
	case civ1.CIMetricsAvailabilityCode_CI_METRICS_AVAILABILITY_CODE_NO_SAMPLES:
		return "no_samples"
	default:
		return ""
	}
}

func metricStatsJSON(stats *civ1.CIMetricsStats) statsJSON {
	return statsJSON{
		SampleCount:              stats.GetSampleCount(),
		CpuSampleCount:           stats.GetCpuSampleCount(),
		MemorySampleCount:        stats.GetMemorySampleCount(),
		PeakCpuUtilization:       stats.PeakCpuUtilization,
		AverageCpuUtilization:    stats.AverageCpuUtilization,
		PeakMemoryUtilization:    stats.PeakMemoryUtilization,
		AverageMemoryUtilization: stats.AverageMemoryUtilization,
		ObservedStartedAt:        stats.GetObservedStartedAt(),
		ObservedFinishedAt:       stats.GetObservedFinishedAt(),
		PeakMemoryUsageBytes:     stats.PeakMemoryUsageBytes,
		AverageMemoryUsageBytes:  stats.AverageMemoryUsageBytes,
	}
}

func metricCapJSON(cap *civ1.CIMetricsCapMetadata) capJSON {
	return capJSON{
		RawSampleCount:         cap.GetRawSampleCount(),
		ReturnedSampleCount:    cap.GetReturnedSampleCount(),
		MaxReturnedSampleCount: cap.GetMaxReturnedSampleCount(),
		Downsampled:            cap.GetDownsampled(),
		DownsampleStrategy:     cap.GetDownsampleStrategy(),
	}
}

func printAttemptMetrics(resp *civ1.GetJobAttemptMetricsResponse, orgFlag string) {
	fmt.Printf("Run: %s (%s)\n", resp.GetRun().GetRunId(), resp.GetRun().GetStatus())
	fmt.Printf("Workflow: %s\n", resp.GetWorkflow().GetWorkflowId())
	fmt.Printf("Job: %s [%s] (%s)\n", resp.GetJob().GetJobId(), resp.GetJob().GetJobKey(), resp.GetJob().GetStatus())
	printAttemptSummary(resp.GetAttempt(), "")
	fmt.Printf("Samples: %d returned / %d raw\n", resp.GetAttempt().GetCap().GetReturnedSampleCount(), resp.GetAttempt().GetCap().GetRawSampleCount())
	if attemptID := resp.GetAttempt().GetAttempt().GetAttemptId(); attemptID != "" {
		fmt.Printf("Full samples: %s --output json\n", metricsCommand(attemptID, orgFlag))
	}
}

func printJobMetrics(resp *civ1.GetJobMetricsResponse, orgFlag string) {
	fmt.Printf("Run: %s (%s)\n", resp.GetRun().GetRunId(), resp.GetRun().GetStatus())
	fmt.Printf("Workflow: %s\n", resp.GetWorkflow().GetWorkflowId())
	fmt.Printf("Job: %s [%s] (%s)\n", resp.GetJob().GetJobId(), resp.GetJob().GetJobKey(), resp.GetJob().GetStatus())
	for _, attempt := range resp.GetAttempts() {
		printAttemptSummaryFields(attempt.GetAttempt(), attempt.GetAvailability(), attempt.GetStats(), metricsCommand(attempt.GetAttempt().GetAttemptId(), orgFlag))
	}
}

func printRunMetrics(resp *civ1.GetRunMetricsResponse, orgFlag string) {
	fmt.Printf("Run: %s (%s)\n", resp.GetRun().GetRunId(), resp.GetRun().GetStatus())
	for _, workflow := range resp.GetWorkflows() {
		fmt.Printf("\nWorkflow: %s (%s)\n", workflow.GetWorkflow().GetWorkflowId(), workflow.GetWorkflow().GetStatus())
		for _, job := range workflow.GetJobs() {
			fmt.Printf("  Job: %s [%s] (%s)\n", job.GetJob().GetJobId(), job.GetJob().GetJobKey(), job.GetJob().GetStatus())
			for _, attempt := range job.GetAttempts() {
				printAttemptSummaryFields(attempt.GetAttempt(), attempt.GetAvailability(), attempt.GetStats(), metricsCommand(attempt.GetAttempt().GetAttemptId(), orgFlag))
			}
		}
	}
}

func printAttemptSummary(attempt *civ1.CIMetricsAttemptMetrics, metricsCommand string) {
	printAttemptSummaryFields(attempt.GetAttempt(), attempt.GetAvailability(), attempt.GetStats(), metricsCommand)
}

func printAttemptSummaryFields(attempt *civ1.CIMetricsAttemptContext, availability *civ1.CIMetricsAvailability, stats *civ1.CIMetricsStats, metricsCommand string) {
	fmt.Printf("  Attempt #%d %s (%s)\n", attempt.GetAttempt(), attempt.GetAttemptId(), attempt.GetStatus())
	fmt.Printf("    Availability: %s\n", metricAvailabilityCode(availability.GetCode()))
	if stats.GetObservedStartedAt() != "" || stats.GetObservedFinishedAt() != "" {
		fmt.Printf("    Observed: %s - %s\n", stats.GetObservedStartedAt(), stats.GetObservedFinishedAt())
	}
	if stats.PeakCpuUtilization != nil || stats.AverageCpuUtilization != nil {
		fmt.Printf("    CPU: peak %s, avg %s (%d samples)\n", formatMetricRatio(stats.PeakCpuUtilization), formatMetricRatio(stats.AverageCpuUtilization), stats.GetCpuSampleCount())
	}
	if stats.PeakMemoryUtilization != nil || stats.AverageMemoryUtilization != nil {
		fmt.Printf("    Memory: peak %s, avg %s (%d samples)\n", formatMetricRatio(stats.PeakMemoryUtilization), formatMetricRatio(stats.AverageMemoryUtilization), stats.GetMemorySampleCount())
	}
	if metricsCommand != "" && attempt.GetAttemptId() != "" {
		fmt.Printf("    Metrics: %s\n", metricsCommand)
		fmt.Printf("    Full samples: %s --output json\n", metricsCommand)
	}
}

func metricsCommand(attemptID, orgFlag string) string {
	return fmt.Sprintf("depot ci metrics %s%s", attemptID, orgFlag)
}

func connectErrorMessage(err error) error {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) && connectErr.Message() != "" {
		return errors.New(connectErr.Message())
	}
	return err
}

func validateMetricsOutput(output string) error {
	if output == "" || output == "text" || output == "json" {
		return nil
	}
	return fmt.Errorf("unsupported output %q (valid: text, json)", output)
}

func metricsOutputJSON(output string) bool {
	return output == "json"
}

func countNonEmpty(values ...string) int {
	count := 0
	for _, value := range values {
		if value != "" {
			count++
		}
	}
	return count
}

func formatMetricRatio(value *float64) string {
	if value == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", *value*100)
}
