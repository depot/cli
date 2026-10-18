package packages

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// tomlDocument edits whole tables in a TOML file as text, so comments and
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

func tomlHeader(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") {
		return "", false
	}
	end := strings.Index(trimmed, "]")
	if end < 0 {
		return "", false
	}
	var parts []string
	for _, part := range strings.Split(trimmed[1:end], ".") {
		parts = append(parts, strings.Trim(strings.TrimSpace(part), `"'`))
	}
	return strings.Join(parts, "."), true
}

func isTOMLTableStart(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "[")
}

func (d *tomlDocument) removeTable(name string) {
	var lines []string
	skipping := false
	for _, line := range d.lines {
		if isTOMLTableStart(line) {
			header, _ := tomlHeader(line)
			skipping = header == name
		}
		if !skipping {
			lines = append(lines, line)
		}
	}
	d.lines = lines
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
