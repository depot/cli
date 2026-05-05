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

	// Image is the post-resolution image ref handed to StartSandbox.
	// Not a YAML field — written by the CLI after build/convert run.
	Image string `yaml:"-"`
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

	// s.Image is the resolved image ref — populated by the CLI either
	// from container.tag (prebuilt) or from the build/convert pipeline.
	// Spec YAML never sets this field directly.
	if s.Image != "" {
		v := s.Image
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

