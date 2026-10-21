package packages

import (
	"fmt"
	"strconv"
	"strings"
)

// rubyConfig edits the flat YAML files that Bundler and RubyGems write: a
// "---" line followed by top-level "key: value" lines and block lists. Lines
// that setup doesn't manage are kept unchanged.
type rubyConfig struct {
	lines []string
}

func parseRubyConfig(data []byte) *rubyConfig {
	text := strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if text == "" {
		return &rubyConfig{lines: []string{"---"}}
	}
	return &rubyConfig{lines: strings.Split(text, "\n")}
}

// keyLine returns the last line that assigns key, because Ruby's YAML loader
// uses the last assignment of a duplicate key.
func (c *rubyConfig) keyLine(key string) int {
	for i := len(c.lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(c.lines[i], key+":") {
			return i
		}
	}
	return -1
}

func (c *rubyConfig) get(key string) (string, bool) {
	i := c.keyLine(key)
	if i < 0 {
		return "", false
	}
	return unquoteRubyValue(strings.TrimSpace(strings.TrimPrefix(c.lines[i], key+":"))), true
}

// set replaces the last assignment of key and removes the others.
func (c *rubyConfig) set(key, value string) {
	entry := key + ": " + strconv.Quote(value)
	i := c.keyLine(key)
	if i < 0 {
		c.lines = append(c.lines, entry)
		return
	}
	c.lines[i] = entry
	for j := i - 1; j >= 0; j-- {
		if strings.HasPrefix(c.lines[j], key+":") {
			c.lines = append(c.lines[:j], c.lines[j+1:]...)
		}
	}
}

func (c *rubyConfig) remove(key string) {
	for i := c.keyLine(key); i >= 0; i = c.keyLine(key) {
		c.lines = append(c.lines[:i], c.lines[i+1:]...)
	}
}

// list returns the block list under key. It returns found false when the key
// is absent.
func (c *rubyConfig) list(key string) (items []string, found bool, err error) {
	i := c.keyLine(key)
	if i < 0 {
		return nil, false, nil
	}
	if strings.TrimSpace(strings.TrimPrefix(c.lines[i], key+":")) != "" {
		return nil, true, fmt.Errorf("%s isn't a block list", key)
	}
	for _, line := range c.lines[i+1 : c.listEnd(i)] {
		if item, ok := listItem(line); ok {
			items = append(items, unquoteRubyValue(item))
		}
	}
	return items, true, nil
}

// listEnd returns the index after the last item of the list that starts at
// line i. Blank and comment lines between items belong to the list.
func (c *rubyConfig) listEnd(i int) int {
	end := i + 1
	for j := i + 1; j < len(c.lines); j++ {
		trimmed := strings.TrimSpace(c.lines[j])
		if _, ok := listItem(c.lines[j]); ok {
			end = j + 1
		} else if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			break
		}
	}
	return end
}

// listItem returns the value of a list item line without a trailing comment.
func listItem(line string) (string, bool) {
	item, ok := strings.CutPrefix(strings.TrimSpace(line), "- ")
	if !ok {
		return "", false
	}
	item = strings.TrimSpace(item)
	if item != "" && (item[0] == '"' || item[0] == '\'') {
		for i := 1; i < len(item); i++ {
			if item[0] == '"' && item[i] == '\\' {
				i++
			} else if item[i] == item[0] {
				return item[:i+1], true
			}
		}
		return item, true
	}
	if value, _, found := strings.Cut(item, " #"); found {
		return strings.TrimSpace(value), true
	}
	return item, true
}

func (c *rubyConfig) setList(key string, items []string) {
	block := []string{key + ":"}
	for _, item := range items {
		block = append(block, "- "+strconv.Quote(item))
	}

	i := c.keyLine(key)
	if i < 0 {
		c.lines = append(c.lines, block...)
		return
	}
	end := c.listEnd(i)
	c.lines = append(c.lines[:i], append(block, c.lines[end:]...)...)

	for j := i - 1; j >= 0; j-- {
		if strings.HasPrefix(c.lines[j], key+":") {
			c.lines = append(c.lines[:j], c.lines[c.listEnd(j):]...)
		}
	}
}

func (c *rubyConfig) bytes() []byte {
	return []byte(strings.Join(c.lines, "\n") + "\n")
}

func unquoteRubyValue(value string) string {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		if value[0] == '"' {
			if unquoted, err := strconv.Unquote(value); err == nil {
				return unquoted
			}
		}
		return value[1 : len(value)-1]
	}
	return value
}

// bundlerKey converts a setting name to the key Bundler stores in its
// configuration file.
func bundlerKey(name string) string {
	key := strings.ReplaceAll(name, ".", "__")
	key = strings.ReplaceAll(key, "-", "___")
	return "BUNDLE_" + strings.ToUpper(key)
}
