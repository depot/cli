// Package sandbox parses the trusted sandbox.depot.yml hook subset consumed by
// sandbox CLI commands.
package sandbox

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxHookTimeoutSeconds = 1<<31 - 1

// Spec is the supported local hook subset of sandbox.depot.yml.
type Spec struct {
	On HooksSpec `yaml:"on,omitempty"`
}

// HooksSpec groups the currently supported lifecycle hook stages.
type HooksSpec struct {
	Exec []HookSpec `yaml:"exec,omitempty"`
	Down []HookSpec `yaml:"down,omitempty"`
}

// HookSpec is one entry in any On.* list. Command is the shell string sent to
// the sandbox RunHook RPC.
//
// HookSpec accepts either object form (`{command: "...", detach: true}`) or
// bare-string shorthand (`- "echo hi"`); the latter is sugar for
// `{command: "echo hi"}`. See UnmarshalYAML.
type HookSpec struct {
	// Command is the shell line to execute. Required.
	Command string `yaml:"command"`
	// Detach=true asks RunHook to spawn the hook in the background.
	Detach bool `yaml:"detach,omitempty"`
	// Name is an optional label used for log filenames and CLI output. If
	// omitted, the CLI substitutes the hook's index.
	Name string `yaml:"name,omitempty"`
	// TimeoutSeconds bounds foreground hook execution. Zero means no timeout.
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty"`
}

// UnmarshalYAML lets HookSpec accept either:
//
//   - echo hello             # bare string
//   - command: echo hello    # object
//     detach: true
func (h *HookSpec) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		h.Command = node.Value
		return nil
	}
	type rawHook HookSpec
	var raw rawHook
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*h = HookSpec(raw)
	return nil
}

// Load reads a spec from an explicit path.
func Load(path string) (*Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s Spec
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &s, nil
}

// inputRefRe matches ${input.foo} (required) or ${input.foo?}
// (optional — substitutes "" if the input is missing).
var inputRefRe = regexp.MustCompile(`\$\{input\.([A-Za-z_][A-Za-z0-9_]*)(\?)?\}`)

// substitute replaces ${input.foo} occurrences with shell-quoted values from
// inputs. An unknown reference returns an error unless the trailing `?` makes
// it optional (then the substitution is the empty string).
func substitute(s string, inputs map[string]string) (string, error) {
	var missing []string
	var quoted []string
	var out strings.Builder
	last := 0
	for _, loc := range inputRefRe.FindAllStringIndex(s, -1) {
		start, end := loc[0], loc[1]
		out.WriteString(s[last:start])
		match := s[start:end]
		groups := inputRefRe.FindStringSubmatch(match)
		key := groups[1]
		optional := groups[2] == "?"
		if context := shellQuoteContext(s[:start]); context != "" {
			quoted = append(quoted, fmt.Sprintf("%s in %s quotes", match, context))
			out.WriteString(match)
			last = end
			continue
		}
		v, ok := inputs[key]
		if !ok {
			if optional {
				last = end
				continue
			}
			missing = append(missing, key)
			out.WriteString(match)
			last = end
			continue
		}
		out.WriteString(shellQuote(v))
		last = end
	}
	out.WriteString(s[last:])
	if len(quoted) > 0 {
		return "", fmt.Errorf("input placeholder(s) cannot be used inside shell quotes: %s", strings.Join(quoted, ", "))
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("missing input(s): %s", strings.Join(missing, ", "))
	}
	return out.String(), nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellQuoteContext(prefix string) string {
	var quote rune
	escaped := false
	for _, r := range prefix {
		if escaped {
			escaped = false
			continue
		}
		switch quote {
		case 0:
			switch r {
			case '\\':
				escaped = true
			case '\'':
				quote = '\''
			case '"':
				quote = '"'
			}
		case '\'':
			if r == '\'' {
				quote = 0
			}
		case '"':
			switch r {
			case '\\':
				escaped = true
			case '"':
				quote = 0
			}
		}
	}
	switch quote {
	case '\'':
		return "single"
	case '"':
		return "double"
	default:
		return ""
	}
}

// ResolveHooks returns the spec's hook stages with ${input.KEY} substitution
// applied to each command. The lifecycle is the caller's responsibility —
// see hooks.go in the cmd/sandbox package for how each stage is fired.
func (s *Spec) ResolveHooks(inputs map[string]string) (HooksSpec, error) {
	if inputs == nil {
		inputs = map[string]string{}
	}
	out := HooksSpec{}
	type stage struct {
		label string
		src   []HookSpec
		dst   *[]HookSpec
	}
	for _, st := range []stage{
		{"on.exec", s.On.Exec, &out.Exec},
		{"on.down", s.On.Down, &out.Down},
	} {
		resolved, err := resolveHookList(st.src, st.label, inputs)
		if err != nil {
			return HooksSpec{}, err
		}
		*st.dst = resolved
	}
	return out, nil
}

// ResolveHookStage returns one hook stage with ${input.KEY} substitution
// applied to each command.
func (s *Spec) ResolveHookStage(label string, hooks []HookSpec, inputs map[string]string) ([]HookSpec, error) {
	if inputs == nil {
		inputs = map[string]string{}
	}
	return resolveHookList(hooks, label, inputs)
}

func resolveHookList(hooks []HookSpec, label string, inputs map[string]string) ([]HookSpec, error) {
	if len(hooks) == 0 {
		return nil, nil
	}
	out := make([]HookSpec, len(hooks))
	for i, h := range hooks {
		if strings.TrimSpace(h.Command) == "" {
			return nil, fmt.Errorf("%s[%d]: command is required", label, i)
		}
		if h.TimeoutSeconds < 0 {
			return nil, fmt.Errorf("%s[%d].timeout_seconds: must be non-negative", label, i)
		}
		if h.TimeoutSeconds > maxHookTimeoutSeconds {
			return nil, fmt.Errorf("%s[%d].timeout_seconds: must be <= %d", label, i, maxHookTimeoutSeconds)
		}
		cmd, err := substitute(h.Command, inputs)
		if err != nil {
			return nil, fmt.Errorf("%s[%d].command: %w", label, i, err)
		}
		h.Command = cmd
		out[i] = h
	}
	return out, nil
}

// ParseInputs converts a slice of "key=value" strings into a map.
func ParseInputs(pairs []string) (map[string]string, error) {
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --set %q, expected KEY=VALUE", p)
		}
		out[k] = v
	}
	return out, nil
}
