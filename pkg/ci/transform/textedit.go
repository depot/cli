package transform

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// source indexes the original YAML so node positions can become byte offsets.
type source struct {
	text string
	// lineStarts[i] is the byte offset of the first byte of line i+1.
	lineStarts []int
}

func newSource(raw []byte) *source {
	text := string(raw)
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return &source{text: text, lineStarts: starts}
}

func (s *source) lineCount() int {
	return len(s.lineStarts)
}

// lineStart returns the byte offset where a line begins. One past the last line
// is accepted as the end of the text.
func (s *source) lineStart(line int) (int, bool) {
	if line < 1 || line > len(s.lineStarts)+1 {
		return 0, false
	}
	if line == len(s.lineStarts)+1 {
		return len(s.text), true
	}
	return s.lineStarts[line-1], true
}

// lineEnd returns the byte offset just past a line's content.
func (s *source) lineEnd(line int) (int, bool) {
	start, ok := s.lineStart(line)
	if !ok {
		return 0, false
	}
	end := len(s.text)
	if line < len(s.lineStarts) {
		end = s.lineStarts[line] - 1 // drop the \n
	}
	if end < start {
		end = start
	}
	return end, true
}

// line returns a line without its line terminator.
func (s *source) line(line int) string {
	start, ok := s.lineStart(line)
	if !ok {
		return ""
	}
	end, ok := s.lineEnd(line)
	if !ok {
		return ""
	}
	return strings.TrimSuffix(s.text[start:end], "\r")
}

// offset converts a YAML line and character column into a byte offset.
func (s *source) offset(line, col int) (int, bool) {
	start, ok := s.lineStart(line)
	if !ok || col < 1 {
		return 0, false
	}
	end, ok := s.lineEnd(line)
	if !ok {
		return 0, false
	}
	off := start
	for c := 1; c < col; c++ {
		if off >= end {
			return 0, false
		}
		_, size := utf8.DecodeRuneInString(s.text[off:])
		off += size
	}
	return off, true
}

// nodeOffset returns the byte offset where a node begins.
func (s *source) nodeOffset(n *yaml.Node) (int, bool) {
	if n == nil {
		return 0, false
	}
	return s.offset(n.Line, n.Column)
}

// indentOf returns a line's leading whitespace width and whether it has content.
func (s *source) indentOf(line int) (indent int, hasContent bool) {
	text := s.line(line)
	trimmed := strings.TrimLeft(text, " \t")
	if trimmed == "" {
		return 0, false
	}
	return len(text) - len(trimmed), true
}

// isCommentLine reports whether a line is a comment at the given indent.
func (s *source) isCommentLine(line, indent int) bool {
	got, hasContent := s.indentOf(line)
	if !hasContent || got != indent {
		return false
	}
	return strings.HasPrefix(strings.TrimLeft(s.line(line), " \t"), "#")
}

// edit replaces the byte range [start,end); equal bounds insert text.
type edit struct {
	start, end int
	text       string
}

// applyEdits splices non-overlapping edits into the text.
func applyEdits(text string, edits []edit) (string, error) {
	sorted := make([]edit, len(edits))
	copy(sorted, edits)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].start != sorted[j].start {
			return sorted[i].start < sorted[j].start
		}
		return sorted[i].end < sorted[j].end
	})

	var b strings.Builder
	last := 0
	for _, e := range sorted {
		if e.start < 0 || e.end < e.start || e.end > len(text) {
			return "", fmt.Errorf("edit [%d,%d) out of range for %d bytes", e.start, e.end, len(text))
		}
		if e.start < last {
			return "", fmt.Errorf("edit [%d,%d) overlaps a previous edit ending at %d", e.start, e.end, last)
		}
		b.WriteString(text[last:e.start])
		b.WriteString(e.text)
		last = e.end
	}
	b.WriteString(text[last:])
	return b.String(), nil
}

// scalarExtent returns a safely delimited scalar token, or false when callers
// should use the re-encoding fallback. flow marks flow-collection delimiters.
func scalarExtent(text string, n *yaml.Node, start int, flow bool) (int, int, bool) {
	if n == nil || n.Kind != yaml.ScalarNode || n.Anchor != "" || n.Alias != nil {
		return 0, 0, false
	}
	if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle|yaml.TaggedStyle) != 0 {
		return 0, 0, false
	}
	if start < 0 || start >= len(text) {
		return 0, 0, false
	}

	var end int
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		if text[start] != '"' {
			return 0, 0, false
		}
		i := start + 1
		for i < len(text) && text[i] != '\n' {
			if text[i] == '\\' {
				i += 2
				continue
			}
			if text[i] == '"' {
				end = i + 1
				break
			}
			i++
		}

	case n.Style&yaml.SingleQuotedStyle != 0:
		if text[start] != '\'' {
			return 0, 0, false
		}
		i := start + 1
		for i < len(text) && text[i] != '\n' {
			if text[i] == '\'' {
				if i+1 < len(text) && text[i+1] == '\'' {
					i += 2
					continue
				}
				end = i + 1
				break
			}
			i++
		}

	default: // plain
		i := start
		for i < len(text) {
			c := text[i]
			if c == '\n' {
				break
			}
			// A comment starts only after whitespace.
			if c == '#' && i > start && (text[i-1] == ' ' || text[i-1] == '\t') {
				break
			}
			// Stop before a mapping key's colon.
			if c == ':' && (i+1 >= len(text) || isSpaceByte(text[i+1]) || text[i+1] == '\n') {
				break
			}
			if flow && (c == ',' || c == ']' || c == '}' || c == ':') {
				break
			}
			i++
		}
		end = i
		for end > start && isSpaceByte(text[end-1]) {
			end--
		}
	}

	if end <= start || end > len(text) {
		return 0, 0, false
	}
	if !scalarSourceIs(text[start:end], n.Value) {
		return 0, 0, false
	}
	return start, end, true
}

// scalarSourceIs confirms an extracted token has the node's value.
func scalarSourceIs(token, want string) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(token), &doc); err != nil {
		return false
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return false
	}
	got := doc.Content[0]
	return got.Kind == yaml.ScalarNode && got.Value == want
}

// commentStart returns a trailing comment offset, or -1 when absent.
func (s *source) commentStart(line, from int) int {
	end, ok := s.lineEnd(line)
	if !ok || from < 0 || from > end {
		return -1
	}
	if idx := strings.IndexByte(s.text[from:end], '#'); idx >= 0 {
		return from + idx
	}
	return -1
}

// contentEnd returns where a trailing comment can be appended.
func (s *source) contentEnd(line int) (int, bool) {
	start, ok := s.lineStart(line)
	if !ok {
		return 0, false
	}
	end, ok := s.lineEnd(line)
	if !ok {
		return 0, false
	}
	for end > start && isSpaceByte(s.text[end-1]) {
		end--
	}
	return end, true
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r'
}
