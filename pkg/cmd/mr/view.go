package mr

import (
	"fmt"
	"io"
	"strings"

	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

// getWithReadiness is get plus the merge readiness of the head, which costs the API a merge analysis.
// The merge request is the one get finds for branch name, or the one with id when name is empty.
func (o *options) getWithReadiness(cmd *cobra.Command, name, id string) (*code.GetMergeRequestResponse, error) {
	if name != "" {
		return o.find(cmd, name, true)
	}
	return o.fetch(cmd, &code.GetMergeRequestRequest{Lookup: &code.GetMergeRequestRequest_MergeRequestId{MergeRequestId: id}, IncludeReadiness: true})
}

func writeView(cmd *cobra.Command, response *code.GetMergeRequestResponse) error {
	summary := proto.Clone(response.MergeRequest).(*code.MergeRequest)
	summary.Body = ""
	summary.Title, summary.HeadRef, summary.BaseRef = printable(summary.Title), printable(summary.HeadRef), printable(summary.BaseRef)
	if err := writeMR(cmd, summary); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if err := writeReadiness(out, response.Readiness); err != nil {
		return err
	}
	if body := printableText(response.MergeRequest.Body); body != "" {
		_, err := fmt.Fprintf(out, "\n%s\n", body)
		return err
	}
	return nil
}

// writeReadiness prints nothing when the API sent no readiness: the merge request is not open, or the API predates it.
func writeReadiness(out io.Writer, r *code.MergeRequestReadiness) error {
	if r == nil {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Readiness: %s", label(r.Verdict, "VERDICT_"))
	if reasons := reasonList(r.Reasons); reasons != "" {
		fmt.Fprintf(&b, " (%s)", reasons)
	}
	fmt.Fprintf(&b, "\nMergeable: %s\n", label(r.Mergeability, "MERGEABILITY_"))
	for _, path := range r.ConflictPaths {
		fmt.Fprintf(&b, "  conflict: %s\n", printable(path))
	}
	if r.ConflictPathsTruncated {
		b.WriteString("  more conflicting paths not shown\n")
	}
	fmt.Fprintf(&b, "CI: %s\n", checkState(r.CiState))
	for _, w := range r.CiWorkflows {
		fmt.Fprintf(&b, "  %s: %s\n", checkState(w.State), printable(w.Name))
	}
	fmt.Fprintf(&b, "Agent checks: %s\n", checkState(r.AgentChecksState))
	for _, c := range r.AgentChecks {
		fmt.Fprintf(&b, "  %s: %s", checkState(c.State), printable(c.AgentName))
		if c.OpenFindings > 0 {
			fmt.Fprintf(&b, " (%d open findings)", c.OpenFindings)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "Approvals for this head: %d\n", len(r.CountingApprovals))
	if r.ChecksIncomplete {
		b.WriteString("Some checks could not be read and may be missing.\n")
	}
	_, err := io.WriteString(out, b.String())
	return err
}

// printableText is printable for text that keeps its line breaks.
func printableText(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = printable(line)
	}
	return strings.Join(lines, "\n")
}

func reasonList(reasons []*code.MergeRequestReadiness_Reason) string {
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		name := label(reason.Kind, "KIND_")
		switch {
		case reason.Kind == code.MergeRequestReadiness_Reason_KIND_IDLE:
			name = fmt.Sprintf("idle %d days", reason.Count)
		case reason.Count > 0:
			name = fmt.Sprintf("%d %s", reason.Count, name)
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

func checkState(state code.MergeRequestCheckState) string {
	if state == code.MergeRequestCheckState_MERGE_REQUEST_CHECK_STATE_UNSPECIFIED {
		return "none"
	}
	return label(state, "MERGE_REQUEST_CHECK_STATE_")
}

// label turns an enum name such as VERDICT_NOT_READY into "not ready".
func label(value fmt.Stringer, prefix string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(value.String(), prefix), "_", " "))
}
