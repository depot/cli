package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
)

const (
	instructionsFile = "AGENTS.md"
	skillsDir        = "skills"
	skillFile        = "SKILL.md"
	pluginsFile      = "plugins.json"
)

const definitionLayout = `A definition directory holds:
  AGENTS.md              instructions added to the agent's system prompt
  skills/<name>/SKILL.md one skill each, with name and description in YAML front matter; skills are sent in name order
  plugins.json           plugins the agent loads, as [{"name": ..., "digest": ...}]
Every file is optional.`

func newCmdPull() *cobra.Command {
	var (
		auth    authFlags
		version uint32
		force   bool
	)

	cmd := &cobra.Command{
		Use:   "pull [flags] <session-id> [dir]",
		Short: "Write a session's agent definition to a directory",
		Long:  "Write a session's agent definition to a directory, ./depot-agent by default.\n\n" + definitionLayout,
		Example: `  # Edit a session's instructions and skills, then push them back
  depot agent pull <session-id>
  $EDITOR depot-agent/AGENTS.md
  depot agent push <session-id>`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "depot-agent"
			if len(args) == 2 {
				dir = args[1]
			}
			s, err := auth.resolve(cmd.Context())
			if err != nil {
				return err
			}
			def, err := getDefinition(cmd.Context(), s, args[0], version, cmd.Flags().Changed("version"))
			if err != nil {
				return err
			}
			if err := writeDefinition(dir, def.GetContent(), force); err != nil {
				return err
			}
			fmt.Printf("Wrote version %d of %s's definition to %s\n", def.GetVersion(), safeText(args[0]), safeText(dir))
			return nil
		},
	}

	auth.register(cmd)
	cmd.Flags().Uint32Var(&version, "version", 0, "Version to pull (defaults to the current one)")
	cmd.Flags().BoolVar(&force, "force", false, "Replace a definition already in the directory")
	return cmd
}

func newCmdPush() *cobra.Command {
	var (
		auth    authFlags
		version uint32
		output  string
	)

	cmd := &cobra.Command{
		Use:   "push [flags] <session-id> [dir]",
		Short: "Store a directory as a new version of a session's agent definition",
		Long: "Store a directory, ./depot-agent by default, as a new version of a session's agent definition.\n" +
			"The agent loads it before its next turn. With --version, store an earlier version again to roll back.\n\n" +
			definitionLayout,
		Example: `  # Push edited instructions and skills
  depot agent push <session-id> ./depot-agent

  # Roll back to version 3
  depot agent push <session-id> --version 3`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			rollback := cmd.Flags().Changed("version")
			if rollback && len(args) == 2 {
				return errors.New("pass a directory or --version, not both")
			}
			s, err := auth.resolve(cmd.Context())
			if err != nil {
				return err
			}
			dir := "depot-agent"
			if len(args) == 2 {
				dir = args[1]
			}
			var from *uint32
			if rollback {
				from = &version
			}
			resp, err := pushDefinition(cmd.Context(), s, args[0], dir, from)
			if err != nil {
				return err
			}
			if output == "json" {
				return writeProtoJSON(resp)
			}
			fmt.Printf("Session %s now runs definition version %d\n", safeText(args[0]), resp.GetDefinition().GetVersion())
			return nil
		},
	}

	auth.register(cmd)
	cmd.Flags().Uint32Var(&version, "version", 0, "Store this earlier version again instead of a directory")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	return cmd
}

// pushDefinition stores dir as the session's next definition version, or, given from, that stored version's content again.
func pushDefinition(ctx context.Context, s *session, sessionID, dir string, from *uint32) (*agentv1.PutAgentDefinitionResponse, error) {
	var content *agentv1.DepotAgentDefinitionContent
	if from != nil {
		def, err := getDefinition(ctx, s, sessionID, *from, true)
		if err != nil {
			return nil, err
		}
		content = def.GetContent()
	} else {
		var err error
		if content, err = readDefinition(dir); err != nil {
			return nil, err
		}
	}
	resp, err := s.client.PutAgentDefinition(ctx, authed(s, &agentv1.PutAgentDefinitionRequest{SessionId: sessionID, Content: content}))
	if err != nil {
		return nil, fmt.Errorf("put definition: %w", err)
	}
	return resp.Msg, nil
}

func getDefinition(ctx context.Context, s *session, sessionID string, version uint32, pinned bool) (*agentv1.DepotAgentDefinition, error) {
	req := &agentv1.GetAgentDefinitionRequest{SessionId: sessionID}
	if pinned {
		req.Version = ptr(version)
	}
	resp, err := s.client.GetAgentDefinition(ctx, authed(s, req))
	if err != nil {
		return nil, fmt.Errorf("get definition: %w", err)
	}
	return resp.Msg.GetDefinition(), nil
}

type skillFrontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type pluginRef struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// writeDefinition refuses to mix a pulled definition into one already there, since push would send the leftovers.
func writeDefinition(dir string, content *agentv1.DepotAgentDefinitionContent, force bool) error {
	for _, name := range []string{instructionsFile, skillsDir, pluginsFile} {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); err == nil {
			if !force {
				return fmt.Errorf("%s already exists; pass --force to replace the definition in %s", path, dir)
			}
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if content.GetInstructions() != "" {
		if err := os.WriteFile(filepath.Join(dir, instructionsFile), []byte(content.GetInstructions()), 0o644); err != nil {
			return err
		}
	}
	for _, skill := range content.GetSkills() {
		if !validSkillName(skill.GetName()) {
			return fmt.Errorf("skill name %q is not a directory name", safeText(skill.GetName()))
		}
		front, err := yaml.Marshal(skillFrontMatter{Name: skill.GetName(), Description: skill.GetDescription()})
		if err != nil {
			return err
		}
		path := filepath.Join(dir, skillsDir, skill.GetName(), skillFile)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		data := slices.Concat([]byte("---\n"), front, []byte("---\n"), []byte(skill.GetBody()))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	if len(content.GetPlugins()) > 0 {
		refs := make([]pluginRef, 0, len(content.GetPlugins()))
		for _, p := range content.GetPlugins() {
			refs = append(refs, pluginRef{Name: p.GetName(), Digest: p.GetDigest()})
		}
		data, err := json.MarshalIndent(refs, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, pluginsFile), append(data, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func readDefinition(dir string) (*agentv1.DepotAgentDefinitionContent, error) {
	if info, err := os.Stat(dir); err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	content := &agentv1.DepotAgentDefinitionContent{}
	if data, err := os.ReadFile(filepath.Join(dir, instructionsFile)); err == nil {
		content.Instructions = string(data)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	entries, err := os.ReadDir(filepath.Join(dir, skillsDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, skillsDir, entry.Name(), skillFile)
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s has no %s", filepath.Join(dir, skillsDir, entry.Name()), skillFile)
		}
		if err != nil {
			return nil, err
		}
		skill, err := parseSkill(entry.Name(), data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		content.Skills = append(content.Skills, skill)
	}

	if data, err := os.ReadFile(filepath.Join(dir, pluginsFile)); err == nil {
		refs, err := parsePlugins(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, pluginsFile), err)
		}
		content.Plugins = refs
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return content, nil
}

// parseSkill splits SKILL.md into its front matter and body; the body is everything after the closing "---" line.
func parseSkill(dirName string, data []byte) (*agentv1.DepotAgentSkill, error) {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return nil, errors.New("must start with YAML front matter holding a description")
	}
	front, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		if front, ok = bytes.CutSuffix(rest, []byte("\n---")); !ok {
			return nil, errors.New("front matter has no closing ---")
		}
	}
	var meta skillFrontMatter
	if err := yaml.Unmarshal(front, &meta); err != nil {
		return nil, fmt.Errorf("front matter: %w", err)
	}
	if meta.Name != "" && meta.Name != dirName {
		return nil, fmt.Errorf("front matter names %q, but its directory is %q", meta.Name, dirName)
	}
	return &agentv1.DepotAgentSkill{Name: dirName, Description: strings.TrimSpace(meta.Description), Body: string(body)}, nil
}

func parsePlugins(data []byte) ([]*agentv1.DepotAgentPluginRef, error) {
	var refs []pluginRef
	if err := json.Unmarshal(data, &refs); err != nil {
		return nil, err
	}
	out := make([]*agentv1.DepotAgentPluginRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, &agentv1.DepotAgentPluginRef{Name: r.Name, Digest: r.Digest})
	}
	return out, nil
}

func validSkillName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && filepath.Base(name) == name
}
