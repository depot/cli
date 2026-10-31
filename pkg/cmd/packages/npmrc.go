package packages

import (
	"strings"
)

type npmrc struct {
	lines []string
}

func parseNpmrc(data []byte) *npmrc {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return &npmrc{}
	}
	return &npmrc{lines: strings.Split(text, "\n")}
}

func npmrcKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
		return "", false
	}
	key, _, found := strings.Cut(trimmed, "=")
	if !found {
		return "", false
	}
	return strings.TrimSpace(key), true
}

// get returns the last assignment of key, which is the value npm uses, with
// INI quotes removed.
func (n *npmrc) get(key string) (string, bool) {
	for i := len(n.lines) - 1; i >= 0; i-- {
		if k, ok := npmrcKey(n.lines[i]); ok && k == key {
			_, value, _ := strings.Cut(n.lines[i], "=")
			return unquoteINIValue(strings.TrimSpace(value)), true
		}
	}
	return "", false
}

func (n *npmrc) set(key, value string) {
	entry := key + "=" + value
	replaced := false
	lines := n.lines[:0]
	for _, line := range n.lines {
		if k, ok := npmrcKey(line); ok && k == key {
			if replaced {
				continue
			}
			line = entry
			replaced = true
		}
		lines = append(lines, line)
	}
	if !replaced {
		lines = append(lines, entry)
	}
	n.lines = lines
}

func (n *npmrc) remove(key string) {
	lines := n.lines[:0]
	for _, line := range n.lines {
		if k, ok := npmrcKey(line); ok && k == key {
			continue
		}
		lines = append(lines, line)
	}
	n.lines = lines
}

func (n *npmrc) bytes() []byte {
	if len(n.lines) == 0 {
		return nil
	}
	return []byte(strings.Join(n.lines, "\n") + "\n")
}

func unquoteINIValue(value string) string {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		return value[1 : len(value)-1]
	}
	return value
}
