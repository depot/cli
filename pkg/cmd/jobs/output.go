package jobs

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func writeJSON(w io.Writer, msg proto.Message) error {
	data, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func cell(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

func status(s corev1.GithubActionsJobStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "GITHUB_ACTIONS_JOB_STATUS_"))
}
func conclusion(c corev1.GithubActionsJobConclusion) string {
	if c == corev1.GithubActionsJobConclusion_GITHUB_ACTIONS_JOB_CONCLUSION_UNSPECIFIED {
		return "-"
	}
	return strings.ToLower(strings.TrimPrefix(c.String(), "GITHUB_ACTIONS_JOB_CONCLUSION_"))
}
func timestamp(t *timestamppb.Timestamp) string {
	if t == nil {
		return "-"
	}
	return t.AsTime().UTC().Format(time.RFC3339)
}

func writePage(w io.Writer, token string, through *timestamppb.Timestamp) error {
	if through != nil {
		if _, err := fmt.Fprintf(w, "Data through: %s\n", timestamp(through)); err != nil {
			return err
		}
	}
	if token != "" {
		_, err := fmt.Fprintf(w, "Next page token: %s\nPass this token with --page-token and keep filters and time range unchanged.\n", cell(token))
		return err
	}
	return nil
}

func writeJobs(w io.Writer, resp *corev1.ListGithubActionsJobsResponse) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(resp.Jobs) == 0 {
		fmt.Fprintln(tw, "No matching GitHub Actions jobs found.")
	} else {
		fmt.Fprintln(tw, "JOB ID\tREPOSITORY\tWORKFLOW\tJOB\tSTATUS\tCONCLUSION\tRUNNER\tDURATION\tQUEUE")
		for _, j := range resp.Jobs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%ds\t%ds\n", cell(j.JobId), cell(j.Repository), cell(j.WorkflowName), cell(j.Name), status(j.Status), conclusion(j.Conclusion), cell(j.RunnerLabel), j.DurationSeconds, j.QueueSeconds)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return writePage(w, resp.GetNextPageToken(), resp.DataThrough)
}

func writeJob(w io.Writer, j *corev1.GithubActionsJob) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"Job ID", j.JobId}, {"Run ID", j.RunId}, {"Repository", j.Repository}, {"Job", j.Name},
		{"Workflow", j.WorkflowName}, {"Workflow path", j.WorkflowPath}, {"Status", status(j.Status)},
		{"Conclusion", conclusion(j.Conclusion)}, {"Runner", j.RunnerLabel},
		{"OS", j.RequestedOs}, {"Architecture", j.RequestedArchitecture},
		{"Requested CPUs", fmt.Sprint(j.RequestedCpuCount)}, {"Requested memory", fmt.Sprintf("%d GiB", j.RequestedMemoryGib)},
		{"Created", timestamp(j.CreatedAt)}, {"Started", timestamp(j.StartedAt)}, {"Finished", timestamp(j.FinishedAt)},
		{"Duration", fmt.Sprintf("%ds", j.DurationSeconds)}, {"Queue time", fmt.Sprintf("%ds", j.QueueSeconds)},
		{"Billable time", fmt.Sprintf("%ds", j.BillableSeconds)}, {"GitHub URL", j.GithubUrl},
	} {
		fmt.Fprintf(tw, "%s:\t%s\n", row[0], cell(row[1]))
	}
	return tw.Flush()
}

func writeAnalytics(w io.Writer, resp *corev1.ListGithubActionsJobAnalyticsResponse) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(resp.Jobs) == 0 {
		fmt.Fprintln(tw, "No matching GitHub Actions job analytics found.")
	} else {
		fmt.Fprintln(tw, "REPOSITORY\tWORKFLOW\tWORKFLOW PATH\tJOB\tRUNNER\tRUNS\tAVG CPU\tPEAK CPU\tAVG MEMORY\tPEAK MEMORY\tAVG DURATION\tP95 DURATION\tFAILURE RATE")
		for _, j := range resp.Jobs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%.1f%%\t%.1f%%\t%.1f%%\t%.1f%%\t%.1fs\t%.1fs\t%.1f%%\n", cell(j.Repository), cell(j.WorkflowName), cell(j.WorkflowPath), cell(j.JobName), cell(j.RunnerLabel), j.JobCount, j.AverageCpuUtilization*100, j.PeakCpuUtilization*100, j.AverageMemoryUtilization*100, j.PeakMemoryUtilization*100, j.AverageDurationSeconds, j.P95DurationSeconds, j.FailureRate*100)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return writePage(w, resp.GetNextPageToken(), resp.DataThrough)
}
