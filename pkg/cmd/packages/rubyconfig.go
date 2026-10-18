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

func (c *rubyConfig) keyLine(key string) int {
	for i, line := range c.lines {
		if strings.HasPrefix(line, key+":") {
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

func (c *rubyConfig) set(key, value string) {
	entry := key + ": " + strconv.Quote(value)
	if i := c.keyLine(key); i >= 0 {
		c.lines[i] = entry
		return
	}
	c.lines = append(c.lines, entry)
}

func (c *rubyConfig) remove(key string) {
	if i := c.keyLine(key); i >= 0 {
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
	for _, line := range c.lines[i+1:] {
		item, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "- ")
		if !ok {
			break
		}
		items = append(items, unquoteRubyValue(strings.TrimSpace(item)))
	}
	return items, true, nil
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
	end := i + 1
	for end < len(c.lines) && strings.HasPrefix(strings.TrimLeft(c.lines[end], " "), "- ") {
		end++
	}
	c.lines = append(c.lines[:i], append(block, c.lines[end:]...)...)
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
