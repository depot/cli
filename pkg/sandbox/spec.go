// Package sandbox provides parsing and translation of the declarative
// `sandbox.depot.yml` spec into StartSandboxRequest messages. The spec is
// inspired by fly.toml: one file declares everything about a sandbox so the
// caller does not have to assemble flags or JSON for each StartSandbox call.
package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"gopkg.in/yaml.v3"
)

// MCPEnvVar is the environment variable the boot process inside the sandbox
// reads to materialize MCP server configs. The value is JSON-encoded MCPSpec.
const MCPEnvVar = "DEPOT_AGENT_MCP_CONFIG"

// DefaultSpecFilenames are the names searched in directory walk-up order.
var DefaultSpecFilenames = []string{"sandbox.depot.yml", "sandbox.depot.yaml"}

// Spec is the on-disk schema for a sandbox.
type Spec struct {
	Name      string            `yaml:"name"`
	AgentType string            `yaml:"agent_type,omitempty"`
	Argv      string            `yaml:"argv,omitempty"`
	Command   string            `yaml:"command,omitempty"`
	Detach    *bool             `yaml:"detach,omitempty"`
	Template  string            `yaml:"template,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Git       *GitSpec          `yaml:"git,omitempty"`
	SSH       *SSHSpec          `yaml:"ssh,omitempty"`
	MCP       *MCPSpec          `yaml:"mcp,omitempty"`
	Container *ContainerSpec    `yaml:"container,omitempty"`

	// On groups every lifecycle hook stage under one key. Stages:
	//   create   — once after the sandbox first boots (one-shot setup).
	//   start    — every boot (fresh up, plus future resume flows).
	//   exec     — before each `depot sandbox exec` user command.
	//   shell    — installed at up time as /etc/profile.d/99-depot-on-shell.sh
	//              so it replaces the login shell when `depot sandbox shell`
	//              opens a pty.
	//   snapshot — before `depot sandbox snapshot` runs the tar pipeline.
	// All stages are sequential; a non-zero exit from a foreground hook
	// aborts the surrounding command.
	On HooksSpec `yaml:"on,omitempty"`

	// Image is the rootfs image ref handed to StartSandbox. Two ways it
	// gets set:
	//   1. User pins a prebuilt ref via the YAML `image:` key (e.g., the
	//      output of `depot sandbox build` or `depot sandbox snapshot`).
	//   2. The CLI's build/convert pipeline derives one from spec.Name +
	//      project + org and overwrites whatever the YAML had.
	//
	// When [container.build] is set, (2) wins — the build product is
	// always preferred over a stale YAML ref. With no build section, the
	// YAML ref is used verbatim. With neither, the API picks a default.
	Image string `yaml:"image,omitempty"`
}

// ContainerSpec describes how to produce the sandbox's rootfs.
//
// Today only Build is supported: the CLI runs `depot build --save` (saves to
// <orgID>.registry.depot.dev/<projectID>:<spec.Name>), runs an ext4 convert
// CI run against it, and hands StartSandbox the bare-host form
// registry.depot.dev/<projectID>:<spec.Name>-ext4 (the API does the org-tenant
// routing internally).
//
// The build/convert destinations are CLI-derived from spec.Name, the build
// project, and the current org — there's no Tag field to override them yet.
type ContainerSpec struct {
	Build *BuildSpec `yaml:"build,omitempty"`
}

// BuildSpec describes how to produce the sandbox's container image when
// `depot sandbox up` runs from a directory holding a Dockerfile. It mirrors
// the relevant `depot build` flags so users can read the yml and predict what
// shells out.
type BuildSpec struct {
	Context    string            `yaml:"context,omitempty"`    // default "."
	Dockerfile string            `yaml:"dockerfile,omitempty"` // default "Dockerfile"
	Target     string            `yaml:"target,omitempty"`     // multistage target
	BuildArgs  map[string]string `yaml:"build_args,omitempty"` // --build-arg
	Push       *PushSpec         `yaml:"push,omitempty"`       // when nil, defaults are filled in
	NoCache    bool              `yaml:"no_cache,omitempty"`
}

// PushSpec controls where the built image is pushed. Both fields default
// (Project from the nearest depot.json, Tag from the spec name + git short sha
// or "latest") so most specs leave this section empty.
type PushSpec struct {
	Project string `yaml:"project,omitempty"`
	Tag     string `yaml:"tag,omitempty"`
}

// HooksSpec groups the lifecycle hook stages. Each list runs sequentially.
// See Spec.On for stage semantics.
type HooksSpec struct {
	Create   []HookSpec `yaml:"create,omitempty"`
	Start    []HookSpec `yaml:"start,omitempty"`
	Exec     []HookSpec `yaml:"exec,omitempty"`
	Shell    []HookSpec `yaml:"shell,omitempty"`
	Snapshot []HookSpec `yaml:"snapshot,omitempty"`
	Down     []HookSpec `yaml:"down,omitempty"`
}

// HookSpec is one entry in any On.* list. Command is a shell string
// run via `bash -lc`; Detach=true backgrounds it with setsid so it outlives
// the exec stream (use for long-running processes like an agent loop).
//
// HookSpec accepts either object form (`{command: "...", detach: true}`) or
// bare-string shorthand (`- "echo hi"`); the latter is sugar for
// `{command: "echo hi"}`. See UnmarshalYAML.
type HookSpec struct {
	// Command is the shell line to execute. Required.
	Command string `yaml:"command"`
	// Detach=true backgrounds the command (setsid + redirect stdio) so the
	// caller's exec stream returns once the process has been spawned, not
	// when it exits. Stdio is captured to /tmp/depot-hook-<name>.log
	// inside the sandbox.
	Detach bool `yaml:"detach,omitempty"`
	// Name is an optional label used for log filenames and CLI output. If
	// omitted, the CLI substitutes the hook's index.
	Name string `yaml:"name,omitempty"`
	// TimeoutSeconds bounds a foreground hook. Ignored when Detach=true
	// (the spawn itself is fast; the backgrounded process has no timeout).
	// Zero means no timeout.
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty"`
}

// UnmarshalYAML lets HookSpec accept either:
//
//	- echo hello             # bare string
//	- command: echo hello    # object
//	  detach: true
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

type GitSpec struct {
	URL    string `yaml:"url"`
	Branch string `yaml:"branch,omitempty"`
	Commit string `yaml:"commit,omitempty"`
	Secret string `yaml:"secret,omitempty"`
}

type SSHSpec struct {
	Enabled        bool  `yaml:"enabled"`
	TimeoutMinutes int32 `yaml:"timeout_minutes,omitempty"`
}

type MCPSpec struct {
	Servers map[string]MCPServer `yaml:"servers" json:"servers"`
}

// MCPServer covers both Claude Code MCP transports:
//   - stdio: `command: ["bin", "arg"]` (subprocess MCP servers, e.g. Plain)
//   - http:  `type: http`, `url: "..."` (hosted MCP servers, e.g. Linear)
//
// The bootstrap script in the agent image translates this into the schema
// `claude --mcp-config` expects.
type MCPServer struct {
	// http transport
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	URL  string `yaml:"url,omitempty" json:"url,omitempty"`
	// stdio transport
	Command []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// optional headers for http transport (e.g., Authorization)
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
}

// Load reads a spec from an explicit path. Use FindSpec for walk-up discovery.
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

// FindSpec walks up from cwd looking for one of DefaultSpecFilenames and
// returns its absolute path. Mirrors pkg/project.FindConfigFileUp.
func FindSpec(cwd string) (string, error) {
	current, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	for {
		for _, name := range DefaultSpecFilenames {
			p := filepath.Join(current, name)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
		current = next
	}
	return "", fmt.Errorf("no %s found from %s upward", DefaultSpecFilenames[0], cwd)
}

// inputRefRe matches ${input.foo} (required) or ${input.foo?}
// (optional — substitutes "" if the input is missing).
var inputRefRe = regexp.MustCompile(`\$\{input\.([A-Za-z_][A-Za-z0-9_]*)(\?)?\}`)

// substitute replaces ${input.foo} occurrences with values from inputs. An
// unknown reference returns an error unless the trailing `?` makes it
// optional (then the substitution is the empty string).
func substitute(s string, inputs map[string]string) (string, error) {
	var missing []string
	out := inputRefRe.ReplaceAllStringFunc(s, func(match string) string {
		groups := inputRefRe.FindStringSubmatch(match)
		key := groups[1]
		optional := groups[2] == "?"
		v, ok := inputs[key]
		if !ok {
			if optional {
				return ""
			}
			missing = append(missing, key)
			return match
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing input(s): %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// ToStartSandboxRequest translates the spec into a proto request, applying
// ${input.foo} substitution from the supplied map. Unknown refs are an error.
func (s *Spec) ToStartSandboxRequest(inputs map[string]string) (*agentv1.StartSandboxRequest, error) {
	if inputs == nil {
		inputs = map[string]string{}
	}

	argv, err := substitute(s.Argv, inputs)
	if err != nil {
		return nil, fmt.Errorf("argv: %w", err)
	}

	req := &agentv1.StartSandboxRequest{
		AgentType: parseAgentType(s.AgentType),
		Argv:      argv,
	}

	// s.Image is the resolved image ref — set by the YAML `image:` key
	// when no build is configured, or overwritten by the build/convert
	// pipeline when [container.build] is present. ${input.foo} is honored
	// here so a demo or template can take the ref via --set without
	// rendering the spec on the fly.
	if s.Image != "" {
		v, err := substitute(s.Image, inputs)
		if err != nil {
			return nil, fmt.Errorf("image: %w", err)
		}
		req.Image = &v
	}
	if s.Command != "" {
		v, err := substitute(s.Command, inputs)
		if err != nil {
			return nil, fmt.Errorf("command: %w", err)
		}
		req.Command = &v
		// detach defaults to true when a command is given. Override with `detach: false`.
		wait := false
		if s.Detach != nil {
			wait = !*s.Detach
		}
		req.WaitForCommand = &wait
	}
	if s.Template != "" {
		v := s.Template
		req.TemplateId = &v
	}

	if len(s.Env) > 0 || s.MCP != nil {
		req.EnvironmentVariables = make(map[string]string, len(s.Env)+1)
		for k, v := range s.Env {
			sub, err := substitute(v, inputs)
			if err != nil {
				return nil, fmt.Errorf("env[%s]: %w", k, err)
			}
			// Empty-value entries are "forward this name from the outer
			// VM's env" — don't overwrite with "" (would clobber CI
			// secrets like PLAIN_API_KEY). The compose YAML still lists
			// the key by name so the inner container picks it up.
			if sub == "" {
				continue
			}
			req.EnvironmentVariables[k] = sub
		}
		if s.MCP != nil {
			b, err := json.Marshal(s.MCP)
			if err != nil {
				return nil, fmt.Errorf("encode mcp: %w", err)
			}
			req.EnvironmentVariables[MCPEnvVar] = string(b)
		}
	}

	if s.Git != nil && s.Git.URL != "" {
		git := &agentv1.StartSandboxRequest_Context_GitContext{
			RepositoryUrl: s.Git.URL,
		}
		if s.Git.Branch != "" {
			v := s.Git.Branch
			git.Branch = &v
		}
		if s.Git.Commit != "" {
			v := s.Git.Commit
			git.CommitHash = &v
		}
		if s.Git.Secret != "" {
			v := s.Git.Secret
			git.SecretName = &v
		}
		req.Context = &agentv1.StartSandboxRequest_Context{
			Context: &agentv1.StartSandboxRequest_Context_Git{Git: git},
		}
	}

	if s.SSH != nil {
		ssh := &agentv1.StartSandboxRequest_SSHConfig{Enabled: s.SSH.Enabled}
		if s.SSH.TimeoutMinutes > 0 {
			v := s.SSH.TimeoutMinutes
			ssh.TimeoutMinutes = &v
		}
		req.SshConfig = ssh
	}

	return req, nil
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
		{"on.create", s.On.Create, &out.Create},
		{"on.start", s.On.Start, &out.Start},
		{"on.exec", s.On.Exec, &out.Exec},
		{"on.shell", s.On.Shell, &out.Shell},
		{"on.snapshot", s.On.Snapshot, &out.Snapshot},
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

func resolveHookList(hooks []HookSpec, label string, inputs map[string]string) ([]HookSpec, error) {
	if len(hooks) == 0 {
		return nil, nil
	}
	out := make([]HookSpec, len(hooks))
	for i, h := range hooks {
		if strings.TrimSpace(h.Command) == "" {
			return nil, fmt.Errorf("%s[%d]: command is required", label, i)
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

func parseAgentType(s string) agentv1.AgentType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "claude_code", "claude-code", "claude":
		return agentv1.AgentType_AGENT_TYPE_CLAUDE_CODE
	default:
		return agentv1.AgentType_AGENT_TYPE_UNSPECIFIED
	}
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
