package builds

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
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

func timestamp(t *timestamppb.Timestamp) string {
	if t == nil {
		return ""
	}
	return t.AsTime().UTC().Format(time.RFC3339Nano)
}

func number(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func percent(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f%%", *v*100)
}

func display(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return strconv.FormatFloat(*v, 'f', 2, 64)
}

var statisticsHeaders = []string{
	"cpu_sample_count", "average_cpu_utilization", "peak_cpu_utilization", "cpu_observed_seconds", "cpu_time_seconds",
	"memory_sample_count", "average_memory_utilization", "peak_memory_utilization", "memory_observed_seconds", "average_memory_bytes", "peak_memory_bytes",
}

func statisticsRow(s *corev1.BuildResourceStatistics) []string {
	if s == nil {
		s = &corev1.BuildResourceStatistics{}
	}
	return []string{
		fmt.Sprint(s.CpuSampleCount), number(s.AverageCpuUtilization), number(s.PeakCpuUtilization), strconv.FormatFloat(s.CpuObservedSeconds, 'f', -1, 64), number(s.CpuTimeSeconds),
		fmt.Sprint(s.MemorySampleCount), number(s.AverageMemoryUtilization), number(s.PeakMemoryUtilization), strconv.FormatFloat(s.MemoryObservedSeconds, 'f', -1, 64), number(s.AverageMemoryBytes), number(s.PeakMemoryBytes),
	}
}

func tableStatistics(w io.Writer, s *corev1.BuildResourceStatistics) {
	if s == nil {
		s = &corev1.BuildResourceStatistics{}
	}
	fmt.Fprintln(w, "METRIC\tAVERAGE\tPEAK\tSAMPLES\tOBSERVED SECONDS")
	fmt.Fprintf(w, "CPU\t%s\t%s\t%d\t%s\n", percent(s.AverageCpuUtilization), percent(s.PeakCpuUtilization), s.CpuSampleCount, fmt.Sprintf("%.2f", s.CpuObservedSeconds))
	fmt.Fprintf(w, "Memory\t%s\t%s\t%d\t%s\n", percent(s.AverageMemoryUtilization), percent(s.PeakMemoryUtilization), s.MemorySampleCount, fmt.Sprintf("%.2f", s.MemoryObservedSeconds))
	fmt.Fprintf(w, "CPU time (seconds): %s\nMemory bytes (avg / peak): %s / %s\n", display(s.CpuTimeSeconds), display(s.AverageMemoryBytes), display(s.PeakMemoryBytes))
}

const coverageNote = "Observed executor work only; missing measurements are n/a. Cache hits, idle gaps, and daemon overhead are unmeasured."

func writeMetrics(w io.Writer, o options, resp *corev1.ListBuildMetricsResponse) error {
	if o.output == "csv" {
		rows := [][]string{append([]string{"project_id", "build_id", "created_at", "finished_at", "hardware", "machine_id", "architecture", "association_start_at", "association_end_at", "cpu_capacity", "memory_capacity_bytes"}, statisticsHeaders...)}
		for _, b := range resp.Builds {
			machines := b.Machines
			if len(machines) == 0 {
				machines = []*corev1.BuildMachineMetrics{{}}
			}
			for _, m := range machines {
				capacity := ""
				if m.CpuCapacity != nil {
					capacity = fmt.Sprint(*m.CpuCapacity)
				}
				row := []string{o.project, b.BuildId, timestamp(b.CreatedAt), timestamp(b.FinishedAt), b.Hardware, m.MachineId, m.Architecture, timestamp(m.StartAt), timestamp(m.EndAt), capacity, number(m.MemoryCapacityBytes)}
				rows = append(rows, append(row, statisticsRow(m.Statistics)...))
			}
		}
		return csv.NewWriter(w).WriteAll(rows)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Project: %s | %s to %s\n%s\n", cell(o.project), cell(o.start), cell(o.end), coverageNote)
	if len(resp.Builds) == 0 {
		fmt.Fprintln(tw, "No completed builds in this range.")
	}
	for _, b := range resp.Builds {
		fmt.Fprintf(tw, "\nBuild: %s | %s | finished %s\n", cell(b.BuildId), cell(b.Hardware), timestamp(b.FinishedAt))
		if len(b.Machines) == 0 {
			fmt.Fprintln(tw, "No recorded machine association; measurements n/a.")
		}
		for _, m := range b.Machines {
			capacity := "n/a"
			if m.CpuCapacity != nil {
				capacity = fmt.Sprint(*m.CpuCapacity)
			}
			fmt.Fprintf(tw, "Machine: %s | %s | CPUs: %s | Memory capacity (bytes): %s\n", cell(m.MachineId), cell(m.Architecture), capacity, display(m.MemoryCapacityBytes))
			tableStatistics(tw, m.Statistics)
		}
	}
	return tw.Flush()
}

func writeAnalytics(w io.Writer, o options, resp *corev1.GetProjectBuildMetricsResponse) error {
	if o.output == "csv" {
		rows := [][]string{append([]string{"project_id", "start_at", "end_at", "scope", "architecture", "build_count", "builds_without_machines"}, statisticsHeaders...)}
		rows = append(rows, append([]string{o.project, o.start, o.end, "project", "", fmt.Sprint(resp.BuildCount), fmt.Sprint(resp.BuildsWithoutMachines)}, statisticsRow(resp.Statistics)...))
		for _, a := range resp.Architectures {
			rows = append(rows, append([]string{o.project, o.start, o.end, "architecture", a.Architecture, "", ""}, statisticsRow(a.Statistics)...))
		}
		return csv.NewWriter(w).WriteAll(rows)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Project: %s | %s to %s\n%s\n", cell(o.project), cell(o.start), cell(o.end), coverageNote)
	fmt.Fprintf(tw, "Completed builds: %d | Without recorded machines: %d\n\nProject summary (duration-weighted):\n", resp.BuildCount, resp.BuildsWithoutMachines)
	tableStatistics(tw, resp.Statistics)
	for _, a := range resp.Architectures {
		fmt.Fprintf(tw, "\nArchitecture: %s\n", cell(a.Architecture))
		tableStatistics(tw, a.Statistics)
	}
	fmt.Fprintln(tw, "\nPeaks are individual build-machine peaks, not simultaneous project-wide totals.")
	return tw.Flush()
}
