package transform

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSourceOffsetCountsRunesNotBytes(t *testing.T) {
	s := newSource([]byte("a: ünïcode\nb: two\n"))

	off, ok := s.offset(1, 5)
	if !ok {
		t.Fatal("offset failed")
	}
	if got := s.text[off]; got != 'n' {
		t.Errorf("offset(1, 5) = byte %d (%q), want the 'n' after the multibyte rune", off, got)
	}

	if off, ok := s.offset(2, 4); !ok || s.text[off:off+3] != "two" {
		t.Errorf("offset(2, 4) = %d, ok=%v", off, ok)
	}
	if _, ok := s.offset(2, 99); ok {
		t.Error("expected a column past the end of the line to fail")
	}
}

func TestSourceLineStartAcceptsOnePastTheEnd(t *testing.T) {
	s := newSource([]byte("one\ntwo\n"))
	if got := s.lineCount(); got != 3 {
		t.Fatalf("lineCount = %d, want 3 (the empty line after the final newline)", got)
	}
	if off, ok := s.lineStart(s.lineCount() + 1); !ok || off != len(s.text) {
		t.Errorf("lineStart(lineCount+1) = %d, ok=%v, want %d", off, ok, len(s.text))
	}
	if _, ok := s.lineStart(s.lineCount() + 2); ok {
		t.Error("expected a line two past the end to fail")
	}
}

func TestApplyEdits(t *testing.T) {
	const text = "hello world"

	got, err := applyEdits(text, []edit{
		{start: 6, end: 11, text: "there"},
		{start: 0, end: 5, text: "goodbye"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "goodbye there" {
		t.Errorf("got %q, want %q", got, "goodbye there")
	}

	got, err = applyEdits(text, []edit{
		{start: 5, end: 5, text: ","},
		{start: 5, end: 5, text: " and"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello, and world" {
		t.Errorf("got %q, want %q", got, "hello, and world")
	}

	if _, err := applyEdits(text, []edit{
		{start: 0, end: 6, text: "x"},
		{start: 3, end: 8, text: "y"},
	}); err == nil {
		t.Error("expected overlapping edits to be rejected")
	}

	if _, err := applyEdits(text, []edit{{start: 0, end: 99, text: "x"}}); err == nil {
		t.Error("expected an out-of-range edit to be rejected")
	}
}

func TestScalarExtent(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		path string
		want string
	}{
		{name: "plain", doc: "k: ubuntu-latest\n", path: "k", want: "ubuntu-latest"},
		{name: "plain with trailing comment", doc: "k: ubuntu-latest # note\n", path: "k", want: "ubuntu-latest"},
		{name: "plain with trailing space", doc: "k: ubuntu-latest  \n", path: "k", want: "ubuntu-latest"},
		{name: "plain containing a hash", doc: "k: build#1\n", path: "k", want: "build#1"},
		{name: "double quoted", doc: `k: "a: b # c"` + "\n", path: "k", want: `"a: b # c"`},
		{name: "double quoted with escape", doc: `k: "say \"hi\""` + "\n", path: "k", want: `"say \"hi\""`},
		{name: "single quoted", doc: "k: 'it''s here'\n", path: "k", want: "'it''s here'"},
		{name: "literal block refused", doc: "k: |\n  line one\n", path: "k", want: ""},
		{name: "folded block refused", doc: "k: >\n  line one\n", path: "k", want: ""},
		{name: "anchored refused", doc: "k: &a ubuntu-latest\n", path: "k", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(tt.doc), &doc); err != nil {
				t.Fatalf("fixture does not parse: %v", err)
			}
			_, _, val := findMappingEntry(doc.Content[0], tt.path)
			if val == nil {
				t.Fatalf("key %q not found", tt.path)
			}

			s := newSource([]byte(tt.doc))
			start, ok := s.nodeOffset(val)
			if !ok {
				t.Fatal("nodeOffset failed")
			}
			start, end, ok := scalarExtent(s.text, val, start, false)
			if tt.want == "" {
				if ok {
					t.Errorf("expected refusal, got %q", s.text[start:end])
				}
				return
			}
			if !ok {
				t.Fatal("scalarExtent refused a scalar it should have delimited")
			}
			if got := s.text[start:end]; got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScalarExtentStopsAtKeyColon(t *testing.T) {
	const doc = "on:\n  push: {}\n"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &node); err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	_, key, _ := findMappingEntry(node.Content[0], "on")
	if key == nil {
		t.Fatal("key not found")
	}

	s := newSource([]byte(doc))
	start, ok := s.nodeOffset(key)
	if !ok {
		t.Fatal("nodeOffset failed")
	}
	start, end, ok := scalarExtent(s.text, key, start, false)
	if !ok {
		t.Fatal("scalarExtent refused a plain key")
	}
	if got := s.text[start:end]; got != "on" {
		t.Errorf("got %q, want %q", got, "on")
	}
}

func TestScalarExtentRefusesAMisdelimitedToken(t *testing.T) {
	s := newSource([]byte("k: ubuntu-latest\n"))
	node := &yaml.Node{Kind: yaml.ScalarNode, Value: "something-else", Line: 1, Column: 4}
	start, _ := s.nodeOffset(node)
	if _, _, ok := scalarExtent(s.text, node, start, false); ok {
		t.Error("expected a token that does not reparse to the node's value to be refused")
	}
}

func TestIsCommentLine(t *testing.T) {
	s := newSource([]byte("# top\n  # nested\nkey: 1\n\n"))
	if !s.isCommentLine(1, 0) {
		t.Error("line 1 is a comment at indent 0")
	}
	if s.isCommentLine(2, 0) {
		t.Error("line 2's comment is at indent 2, not 0")
	}
	if !s.isCommentLine(2, 2) {
		t.Error("line 2 is a comment at indent 2")
	}
	if s.isCommentLine(3, 0) || s.isCommentLine(4, 0) {
		t.Error("neither a key line nor a blank line is a comment line")
	}
}
