package mr

import (
	"fmt"
	"strings"

	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
)

// overlapPaths is how many shared paths are printed for each overlapping merge request.
const overlapPaths = 5

// overlapText lists the other open merge requests that change the same files.
// It is empty when the API found none, or sent no overlaps at all.
func overlapText(overlaps *code.MergeRequestOverlaps) string {
	found := overlaps.GetMergeRequests()
	var b strings.Builder
	if total := len(found) + int(overlaps.GetOmittedMergeRequestCount()); total > 0 {
		fmt.Fprintf(&b, "Also touching these files: %s\n", count(total, "other open merge request"))
	}
	for _, other := range found {
		fmt.Fprintf(&b, "  %s: %s (%s into %s)\n", printable(other.MergeRequestId), printable(other.Title), printable(other.HeadRef), printable(other.BaseRef))
		shown := other.Paths[:min(len(other.Paths), overlapPaths)]
		for _, path := range shown {
			fmt.Fprintf(&b, "    %s\n", printable(path))
		}
		if rest := len(other.Paths) - len(shown) + int(other.OmittedPathCount); rest > 0 {
			fmt.Fprintf(&b, "    and %s\n", count(rest, "more file"))
		}
	}
	if rest := int(overlaps.GetOmittedMergeRequestCount()); rest > 0 && len(found) > 0 {
		fmt.Fprintf(&b, "  and %s\n", count(rest, "more merge request"))
	} else if rest > 0 {
		b.WriteString("  not listed here\n")
	}
	if overlaps.GetIncomplete() {
		b.WriteString("Some open merge requests could not be compared, so others may also touch these files.\n")
	}
	return b.String()
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
