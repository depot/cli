package transform

import (
	"fmt"
	"strings"

	"github.com/depot/cli/pkg/ci/compat"
	"github.com/depot/cli/pkg/ci/migrate"
	"gopkg.in/yaml.v3"
)

// transformInPlace applies changes to the original YAML and reports whether all
// required edits were safely expressible as source edits.
func transformInPlace(s *source, root *yaml.Node, disabledJobs map[string]disabledJobInfo) ([]byte, []ChangeRecord, bool) {
	var edits []edit
	var changes []ChangeRecord

	for _, plan := range []func(*source, *yaml.Node, map[string]disabledJobInfo) ([]edit, []ChangeRecord, bool){
		planTriggerEdits,
		planRunsOnEdits,
		planDisabledJobEdits,
	} {
		planEdits, planChanges, ok := plan(s, root, disabledJobs)
		if !ok {
			return nil, nil, false
		}
		edits = append(edits, planEdits...)
		changes = append(changes, planChanges...)
	}

	out, err := applyEdits(s.text, edits)
	if err != nil {
		return nil, nil, false
	}

	// Splicing text cannot be trusted on inspection alone: if the result no
	// longer parses, some extent was wrong and re-encoding is the safe answer.
	var check yaml.Node
	if err := yaml.Unmarshal([]byte(out), &check); err != nil {
		return nil, nil, false
	}

	return []byte(out), changes, true
}

// documentEnd returns the exclusive line bound for the document.
func documentEnd(s *source) int {
	return s.lineCount() + 1
}

// findMappingEntry finds a mapping pair and its index.
func findMappingEntry(mapping *yaml.Node, key string) (pairIndex int, keyNode, valNode *yaml.Node) {
	if mapping.Kind != yaml.MappingNode {
		return -1, nil, nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return i / 2, k, mapping.Content[i+1]
		}
	}
	return -1, nil, nil
}

// startLineOf finds an entry's line, including same-indent comments above it.
func startLineOf(s *source, line, indent int) (int, bool) {
	got, hasContent := s.indentOf(line)
	if !hasContent || got != indent {
		return 0, false
	}
	for line > 1 && s.isCommentLine(line-1, indent) {
		line--
	}
	return line, true
}

// boundAfter finds the first line after an entry, preserving separating blanks.
func boundAfter(s *source, next *yaml.Node, nextIndent int, firstLine, outerBound int) int {
	bound := outerBound
	if next != nil {
		bound = next.Line
		for bound > 1 && s.isCommentLine(bound-1, nextIndent) {
			bound--
		}
	}
	for bound-1 > firstLine {
		if _, hasContent := s.indentOf(bound - 1); hasContent {
			break
		}
		bound--
	}
	return bound
}

// mappingEntryRange returns the source line range for a mapping entry.
func mappingEntryRange(s *source, mapping *yaml.Node, pairIndex, outerBound int) (first, bound int, ok bool) {
	key := mapping.Content[2*pairIndex]
	indent := key.Column - 1
	first, ok = startLineOf(s, key.Line, indent)
	if !ok {
		return 0, 0, false
	}
	var next *yaml.Node
	if 2*(pairIndex+1) < len(mapping.Content) {
		next = mapping.Content[2*(pairIndex+1)]
	}
	return first, boundAfter(s, next, indent, first, outerBound), true
}

// sequenceItemRange returns the source line range for a block sequence item.
func sequenceItemRange(s *source, seq *yaml.Node, index, outerBound int) (first, bound int, ok bool) {
	item := seq.Content[index]
	indent, hasContent := s.indentOf(item.Line)
	if !hasContent {
		return 0, 0, false
	}
	first, ok = startLineOf(s, item.Line, indent)
	if !ok {
		return 0, 0, false
	}
	var next *yaml.Node
	if index+1 < len(seq.Content) {
		next = seq.Content[index+1]
	}
	return first, boundAfter(s, next, indent, first, outerBound), true
}

// deleteEntryLines removes an entry without leaving an accidental double gap.
func deleteEntryLines(s *source, first, bound int) (edit, bool) {
	if first > 1 && bound <= s.lineCount() {
		_, afterHasContent := s.indentOf(bound)
		_, beforeHasContent := s.indentOf(first - 1)
		if !afterHasContent && !beforeHasContent {
			first--
		}
	}
	return deleteLines(s, first, bound)
}

// deleteLines returns an edit removing the given line range.
func deleteLines(s *source, first, bound int) (edit, bool) {
	start, ok := s.lineStart(first)
	if !ok {
		return edit{}, false
	}
	end, ok := s.lineStart(bound)
	if !ok {
		return edit{}, false
	}
	if end < start {
		return edit{}, false
	}
	return edit{start: start, end: end, text: ""}, true
}

// planTriggerEdits removes unsupported triggers and records the reason.
func planTriggerEdits(s *source, root *yaml.Node, _ map[string]disabledJobInfo) ([]edit, []ChangeRecord, bool) {
	pairIndex, onKey, onVal := findMappingEntry(root, "on")
	if onKey == nil {
		// yaml.v3 may decode bare `on` as boolean true
		pairIndex, onKey, onVal = findMappingEntry(root, "true")
	}
	if onKey == nil || onVal == nil {
		return nil, nil, true
	}

	var edits []edit
	var changes []ChangeRecord
	var notes []string

	removed := func(trigger string) {
		rule := compat.TriggerRules[trigger]
		notes = append(notes, fmt.Sprintf("Removed unsupported trigger: %s. %s", trigger, rule.Note))
		changes = append(changes, ChangeRecord{
			Type:   ChangeTriggerRemoved,
			Detail: fmt.Sprintf("Removed unsupported trigger %q", trigger),
		})
	}

	onBound := boundAfter(s, nextSibling(root, pairIndex), onKey.Column-1, onKey.Line, documentEnd(s))

	switch onVal.Kind {
	case yaml.ScalarNode:
		if !isUnsupportedTrigger(onVal.Value) {
			return nil, nil, true
		}
		start, ok := s.nodeOffset(onVal)
		if !ok {
			return nil, nil, false
		}
		_, end, ok := scalarExtent(s.text, onVal, start, false)
		if !ok {
			return nil, nil, false
		}
		edits = append(edits, edit{start: start, end: end, text: "{}"})
		removed(onVal.Value)

	case yaml.SequenceNode:
		var drop []int
		for i, item := range onVal.Content {
			if item.Kind == yaml.ScalarNode && isUnsupportedTrigger(item.Value) {
				drop = append(drop, i)
			}
		}
		if len(drop) == 0 {
			return nil, nil, true
		}

		if onVal.Style&yaml.FlowStyle != 0 {
			// Reuse kept source tokens so their quoting survives.
			e, ok := rebuildFlowSequence(s, onVal, drop)
			if !ok {
				return nil, nil, false
			}
			edits = append(edits, e)
		} else if len(drop) == len(onVal.Content) {
			collapse, ok := replaceValueWithEmptyMap(s, onKey, onVal, onBound)
			if !ok {
				return nil, nil, false
			}
			edits = append(edits, collapse...)
		} else {
			for _, i := range drop {
				first, bound, ok := sequenceItemRange(s, onVal, i, onBound)
				if !ok {
					return nil, nil, false
				}
				e, ok := deleteEntryLines(s, first, bound)
				if !ok {
					return nil, nil, false
				}
				edits = append(edits, e)
			}
		}
		for _, i := range drop {
			removed(onVal.Content[i].Value)
		}

	case yaml.MappingNode:
		if onVal.Style&yaml.FlowStyle != 0 {
			// Re-encoding handles flow mappings safely.
			for i := 0; i+1 < len(onVal.Content); i += 2 {
				if k := onVal.Content[i]; k.Kind == yaml.ScalarNode && isUnsupportedTrigger(k.Value) {
					return nil, nil, false
				}
			}
			return nil, nil, true
		}

		var drop []int
		pairs := len(onVal.Content) / 2
		for p := 0; p < pairs; p++ {
			if k := onVal.Content[2*p]; k.Kind == yaml.ScalarNode && isUnsupportedTrigger(k.Value) {
				drop = append(drop, p)
			}
		}
		if len(drop) == 0 {
			return nil, nil, true
		}

		if len(drop) == pairs {
			collapse, ok := replaceValueWithEmptyMap(s, onKey, onVal, onBound)
			if !ok {
				return nil, nil, false
			}
			edits = append(edits, collapse...)
		} else {
			for _, p := range drop {
				first, bound, ok := mappingEntryRange(s, onVal, p, onBound)
				if !ok {
					return nil, nil, false
				}
				e, ok := deleteEntryLines(s, first, bound)
				if !ok {
					return nil, nil, false
				}
				edits = append(edits, e)
			}
		}
		for _, p := range drop {
			removed(onVal.Content[2*p].Value)
		}

	default:
		return nil, nil, true
	}

	if len(notes) > 0 {
		e, ok := insertCommentsAbove(s, onKey.Line, onKey.Column-1, notes)
		if !ok {
			return nil, nil, false
		}
		edits = append(edits, e)
	}

	return edits, changes, true
}

// nextSibling returns the key node after pairIndex.
func nextSibling(mapping *yaml.Node, pairIndex int) *yaml.Node {
	if pairIndex < 0 || 2*(pairIndex+1) >= len(mapping.Content) {
		return nil
	}
	return mapping.Content[2*(pairIndex+1)]
}

// rebuildFlowSequence replaces a flow sequence while preserving kept tokens.
func rebuildFlowSequence(s *source, seq *yaml.Node, drop []int) (edit, bool) {
	start, ok := s.nodeOffset(seq)
	if !ok || start >= len(s.text) || s.text[start] != '[' {
		return edit{}, false
	}

	dropped := make(map[int]bool, len(drop))
	for _, i := range drop {
		dropped[i] = true
	}

	var kept []string
	lastEnd := start + 1
	for i, item := range seq.Content {
		itemStart, ok := s.nodeOffset(item)
		if !ok {
			return edit{}, false
		}
		_, itemEnd, ok := scalarExtent(s.text, item, itemStart, true)
		if !ok {
			return edit{}, false
		}
		lastEnd = itemEnd
		if !dropped[i] {
			kept = append(kept, s.text[itemStart:itemEnd])
		}
	}

	closing := strings.IndexByte(s.text[lastEnd:], ']')
	if closing < 0 {
		return edit{}, false
	}
	end := lastEnd + closing + 1

	text := "{}"
	if len(kept) > 0 {
		text = "[" + strings.Join(kept, ", ") + "]"
	}
	return edit{start: start, end: end, text: text}, true
}

// replaceValueWithEmptyMap collapses a block value to `{}` while preserving the
// key line's trailing comment.
func replaceValueWithEmptyMap(s *source, key, val *yaml.Node, bound int) ([]edit, bool) {
	keyStart, ok := s.nodeOffset(key)
	if !ok {
		return nil, false
	}
	_, keyEnd, ok := scalarExtent(s.text, key, keyStart, false)
	if !ok {
		return nil, false
	}
	colon := keyEnd
	for colon < len(s.text) && isSpaceByte(s.text[colon]) {
		colon++
	}
	if colon >= len(s.text) || s.text[colon] != ':' {
		return nil, false
	}

	if val.Line == key.Line {
		return nil, false // inline collection; re-encoding handles it
	}

	del, ok := deleteLines(s, key.Line+1, bound)
	if !ok {
		return nil, false
	}
	return []edit{
		{start: colon + 1, end: colon + 1, text: " {}"},
		del,
	}, true
}

// insertCommentsAbove inserts same-indent comments immediately above a line.
func insertCommentsAbove(s *source, line, indent int, notes []string) (edit, bool) {
	at, ok := s.lineStart(line)
	if !ok {
		return edit{}, false
	}
	pad := strings.Repeat(" ", indent)
	var b strings.Builder
	for _, note := range notes {
		b.WriteString(pad)
		b.WriteString("# ")
		b.WriteString(note)
		b.WriteString("\n")
	}
	return edit{start: at, end: at, text: b.String()}, true
}

func isUnsupportedTrigger(trigger string) bool {
	rule, ok := compat.TriggerRules[trigger]
	return ok && rule.Supported == compat.Unsupported
}

// planRunsOnEdits remaps runs-on labels and annotates each replacement.
func planRunsOnEdits(s *source, root *yaml.Node, disabledJobs map[string]disabledJobInfo) ([]edit, []ChangeRecord, bool) {
	_, _, jobsVal := findMappingEntry(root, "jobs")
	if jobsVal == nil || jobsVal.Kind != yaml.MappingNode {
		return nil, nil, true
	}

	var edits []edit
	var changes []ChangeRecord
	// Group notes by line so a flow sequence gets one trailing comment.
	noteLines := make([]int, 0, 4)
	notesByLine := make(map[int][]string)
	tokenEndByLine := make(map[int]int)

	for i := 0; i+1 < len(jobsVal.Content); i += 2 {
		jobKey := jobsVal.Content[i]
		jobVal := jobsVal.Content[i+1]
		if jobKey.Kind != yaml.ScalarNode || jobVal.Kind != yaml.MappingNode {
			continue
		}
		if _, disabled := disabledJobs[jobKey.Value]; disabled {
			continue
		}

		_, _, runsOnVal := findMappingEntry(jobVal, "runs-on")
		if runsOnVal == nil {
			continue
		}

		var items []*yaml.Node
		flow := false
		switch runsOnVal.Kind {
		case yaml.ScalarNode:
			items = []*yaml.Node{runsOnVal}
		case yaml.SequenceNode:
			items = runsOnVal.Content
			flow = runsOnVal.Style&yaml.FlowStyle != 0
		default:
			continue
		}

		for _, item := range items {
			if item.Kind != yaml.ScalarNode {
				continue
			}
			original := item.Value
			newLabel, changed, reason := migrate.MapLabel(original)
			if !changed {
				continue
			}

			start, ok := s.nodeOffset(item)
			if !ok {
				return nil, nil, false
			}
			_, end, ok := scalarExtent(s.text, item, start, flow)
			if !ok {
				return nil, nil, false
			}
			edits = append(edits, edit{start: start, end: end, text: quoteLike(item, newLabel)})

			if _, seen := notesByLine[item.Line]; !seen {
				noteLines = append(noteLines, item.Line)
			}
			notesByLine[item.Line] = append(notesByLine[item.Line], fmt.Sprintf("was: %s. %s", original, reason))
			if end > tokenEndByLine[item.Line] {
				tokenEndByLine[item.Line] = end
			}

			changes = append(changes, ChangeRecord{
				Type:    ChangeRunsOn,
				JobName: jobKey.Value,
				Detail:  fmt.Sprintf("Changed runs-on from %q to %q in job %q", original, newLabel, jobKey.Value),
			})
		}
	}

	for _, line := range noteLines {
		e, ok := annotateLine(s, line, tokenEndByLine[line], notesByLine[line])
		if !ok {
			return nil, nil, false
		}
		edits = append(edits, e)
	}

	return edits, changes, true
}

// quoteLike keeps the source scalar's quoting style when safe.
func quoteLike(n *yaml.Node, value string) string {
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0 && !strings.ContainsAny(value, "\"\\\n"):
		return `"` + value + `"`
	case n.Style&yaml.SingleQuotedStyle != 0 && !strings.ContainsAny(value, "'\n"):
		return "'" + value + "'"
	default:
		return value
	}
}

// commentSafe prevents source text from escaping a generated YAML comment.
func commentSafe(notes []string) []string {
	flatten := func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}
	out := make([]string, len(notes))
	for i, note := range notes {
		out[i] = strings.Map(flatten, note)
	}
	return out
}

// annotateLine appends notes unless the line already has a comment.
func annotateLine(s *source, line, tokenEnd int, notes []string) (edit, bool) {
	notes = commentSafe(notes)
	if s.commentStart(line, tokenEnd) >= 0 {
		indent, hasContent := s.indentOf(line)
		if !hasContent {
			return edit{}, false
		}
		return insertCommentsAbove(s, line, indent, notes)
	}
	at, ok := s.contentEnd(line)
	if !ok {
		return edit{}, false
	}
	return edit{start: at, end: at, text: " # " + strings.Join(notes, " ")}, true
}

// planDisabledJobEdits comments out jobs migration cannot correct.
func planDisabledJobEdits(s *source, root *yaml.Node, disabledJobs map[string]disabledJobInfo) ([]edit, []ChangeRecord, bool) {
	if len(disabledJobs) == 0 {
		return nil, nil, true
	}

	jobsIndex, jobsKey, jobsVal := findMappingEntry(root, "jobs")
	if jobsVal == nil || jobsVal.Kind != yaml.MappingNode {
		return nil, nil, false
	}
	jobsBound := boundAfter(s, nextSibling(root, jobsIndex), jobsKey.Column-1, jobsKey.Line, documentEnd(s))

	var edits []edit
	var changes []ChangeRecord

	pairs := len(jobsVal.Content) / 2
	for p := 0; p < pairs; p++ {
		jobKey := jobsVal.Content[2*p]
		if jobKey.Kind != yaml.ScalarNode {
			continue
		}
		info, disabled := disabledJobs[jobKey.Value]
		if !disabled {
			continue
		}

		first, bound, ok := mappingEntryRange(s, jobsVal, p, jobsBound)
		if !ok {
			return nil, nil, false
		}
		start, ok := s.lineStart(first)
		if !ok {
			return nil, nil, false
		}
		end, ok := s.lineStart(bound)
		if !ok {
			return nil, nil, false
		}

		indent := strings.Repeat(" ", jobKey.Column-1)
		var b strings.Builder
		fmt.Fprintf(&b, "%s# DISABLED: %s\n", indent, info.Reason)
		for line := first; line < bound; line++ {
			text := s.line(line)
			if strings.TrimSpace(text) == "" {
				b.WriteString("\n")
				continue
			}
			b.WriteString(indent)
			b.WriteString("# ")
			b.WriteString(text)
			b.WriteString("\n")
		}

		edits = append(edits, edit{start: start, end: end, text: b.String()})
		changes = append(changes, ChangeRecord{
			Type:    ChangeJobDisabled,
			JobName: jobKey.Value,
			Detail:  fmt.Sprintf("Disabled job %q: %s", jobKey.Value, info.Reason),
		})
	}

	return edits, changes, true
}
