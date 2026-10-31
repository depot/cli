package packages

import (
	"os"
	"regexp"
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
// INI quotes removed and ${VAR} references expanded as npm does.
func (n *npmrc) get(key string) (string, bool) {
	for i := len(n.lines) - 1; i >= 0; i-- {
		if k, ok := npmrcKey(n.lines[i]); ok && k == key {
			_, value, _ := strings.Cut(n.lines[i], "=")
			return expandNpmrcEnv(unquoteINIValue(stripINIComment(strings.TrimSpace(value)))), true
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

var npmrcEnvReference = regexp.MustCompile(`\$\{([^}]+)\}`)

// expandNpmrcEnv expands ${VAR} references. Like npm, it leaves a reference
// to an unset variable unchanged.
func expandNpmrcEnv(value string) string {
	return npmrcEnvReference.ReplaceAllStringFunc(value, func(reference string) string {
		if value, ok := os.LookupEnv(reference[2 : len(reference)-1]); ok {
			return value
		}
		return reference
	})
}

// stripINIComment removes an inline comment, which starts with ; or # after
// whitespace, from an unquoted value.
func stripINIComment(value string) string {
	if value != "" && (value[0] == '"' || value[0] == '\'') {
		return value
	}
	for i := 1; i < len(value); i++ {
		if (value[i] == '#' || value[i] == ';') && (value[i-1] == ' ' || value[i-1] == '\t') {
			return strings.TrimSpace(value[:i])
		}
	}
	return value
}

func unquoteINIValue(value string) string {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		return value[1 : len(value)-1]
	}
	return value
}
