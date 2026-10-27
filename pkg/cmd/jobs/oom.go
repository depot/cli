package jobs

import (
	"fmt"
	"io"
	"text/tabwriter"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
)

func writeOOM(w io.Writer, oom *corev1.GithubActionsJobOOM) error {
	status := "unavailable"
	if oom != nil && oom.Detected != nil {
		status = "none observed (does not rule out OOM)"
		if oom.GetDetected() {
			status = "detected"
		}
	} else if oom.GetUnavailableReason() != "" {
		status += " (" + cell(oom.GetUnavailableReason()) + ")"
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "OOM:\t%s\n", status)
	for _, event := range oom.GetEvents() {
		fmt.Fprintf(tw, "OOM event:\t%s PID %d %s\n", timestamp(event.GetTimestamp()), event.GetProcessPid(), cell(event.GetProcessName()))
	}
	if oom.GetTruncated() {
		fmt.Fprintln(tw, "OOM events truncated: only the first 100 are shown.")
	}
	return tw.Flush()
}
