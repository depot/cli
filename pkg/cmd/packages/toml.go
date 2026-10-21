package packages

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// tomlDocument edits tables and keys in a TOML file as text, so comments and
// formatting elsewhere in the file are preserved.
type tomlDocument struct {
	lines []string
}

func parseTOMLDocument(data []byte) (*tomlDocument, error) {
	var check map[string]any
	if err := toml.Unmarshal(data, &check); err != nil {
		return nil, err
	}

	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return &tomlDocument{}, nil
	}
	return &tomlDocument{lines: strings.Split(text, "\n")}, nil
}

// tomlHeader returns the key parts of a table header line. A quoted part can
// contain dots.
func tomlHeader(line string) ([]string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") {
		return nil, false
	}

	var parts []string
	var part strings.Builder
	var quote byte
	for i := 1; i < len(trimmed); i++ {
		c := trimmed[i]
		switch {
		case quote != 0 && c == '\\' && quote == '"' && i+1 < len(trimmed):
			i++
			part.WriteByte(trimmed[i])
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			part.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
		case c == '.' || c == ']':
			parts = append(parts, strings.TrimSpace(part.String()))
			part.Reset()
			if c == ']' {
				return parts, true
			}
		default:
			part.WriteByte(c)
		}
	}
	return nil, false
}

func isTOMLTableStart(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "[")
}

// tableRange returns the line range of the table, from its header to the
// line before the next table, or ok false when the file doesn't have it.
func (d *tomlDocument) tableRange(table []string) (start, end int, ok bool) {
	start = -1
	for i, line := range d.lines {
		if !isTOMLTableStart(line) {
			continue
		}
		if start >= 0 {
			return start, i, true
		}
		if header, _ := tomlHeader(line); slices.Equal(header, table) {
			start = i
		}
	}
	return start, len(d.lines), start >= 0
}

func (d *tomlDocument) removeTable(table ...string) {
	if start, end, ok := d.tableRange(table); ok {
		d.lines = append(d.lines[:start], d.lines[end:]...)
	}
}

func tomlKeyLine(line, key string) bool {
	name, _, found := strings.Cut(line, "=")
	return found && strings.Trim(strings.TrimSpace(name), `"'`) == key
}

// setTableKey sets key in the table's own section, and adds the table when
// the file doesn't have it.
func (d *tomlDocument) setTableKey(table []string, key, value string) {
	entry := key + " = " + value
	start, end, ok := d.tableRange(table)
	if !ok {
		d.appendTable(strings.Join(table, "."), [][2]string{{key, value}})
		return
	}
	for i := start + 1; i < end; i++ {
		if tomlKeyLine(d.lines[i], key) {
			d.lines[i] = entry
			return
		}
	}
	d.lines = append(d.lines[:start+1], append([]string{entry}, d.lines[start+1:]...)...)
}

// removeTableKey removes key from the table's section, and removes the table
// when no other keys remain.
func (d *tomlDocument) removeTableKey(table []string, key string) {
	start, end, ok := d.tableRange(table)
	if !ok {
		return
	}
	empty := true
	for i := end - 1; i > start; i-- {
		trimmed := strings.TrimSpace(d.lines[i])
		if tomlKeyLine(d.lines[i], key) {
			d.lines = append(d.lines[:i], d.lines[i+1:]...)
		} else if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			empty = false
		}
	}
	if empty {
		d.removeTable(table...)
	}
}

func (d *tomlDocument) appendTable(name string, entries [][2]string) {
	for len(d.lines) > 0 && strings.TrimSpace(d.lines[len(d.lines)-1]) == "" {
		d.lines = d.lines[:len(d.lines)-1]
	}
	if len(d.lines) > 0 {
		d.lines = append(d.lines, "")
	}
	d.lines = append(d.lines, "["+name+"]")
	for _, entry := range entries {
		d.lines = append(d.lines, entry[0]+" = "+entry[1])
	}
}

func (d *tomlDocument) values() (map[string]any, error) {
	var values map[string]any
	err := toml.Unmarshal(d.bytes(), &values)
	return values, err
}

func (d *tomlDocument) bytes() []byte {
	if len(d.lines) == 0 {
		return nil
	}
	return []byte(strings.Join(d.lines, "\n") + "\n")
}

func tomlString(s string) string {
	return strconv.Quote(s)
}

func tomlStringArray(values ...string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = tomlString(v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func tomlLookup(values map[string]any, path ...string) (any, bool) {
	var current any = values
	for _, key := range path {
		table, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = table[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func errUnmanagedTOML(path, table string) error {
	return fmt.Errorf("%s defines %s in a form setup can't update; remove it and rerun this command", path, table)
}
