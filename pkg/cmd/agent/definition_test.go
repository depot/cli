package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
)

func sampleDefinition() *agentv1.DepotAgentDefinitionContent {
	return &agentv1.DepotAgentDefinitionContent{
		Instructions: "Be brief.\n",
		Skills: []*agentv1.DepotAgentSkill{
			{Name: "release-notes", Description: "Use when: writing release notes", Body: "# Notes\n---\nkeep the rule above\n"},
			{Name: "triage", Description: "Sort new issues", Body: ""},
		},
		Plugins: []*agentv1.DepotAgentPluginRef{{Name: "lint", Digest: "sha256:abc"}},
	}
}

func TestDefinitionRoundTripsThroughADirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "def")
	want := sampleDefinition()
	if err := writeDefinition(dir, want, false); err != nil {
		t.Fatalf("writeDefinition: %v", err)
	}
	got, err := readDefinition(dir)
	if err != nil {
		t.Fatalf("readDefinition: %v", err)
	}
	if !proto.Equal(got, want) {
		t.Fatalf("round trip changed the definition:\n got %v\nwant %v", got, want)
	}
}

func TestDefinitionDirectoryHoldsSkillsInNameOrder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "def")
	content := sampleDefinition()
	slices.Reverse(content.Skills)
	if err := writeDefinition(dir, content, false); err != nil {
		t.Fatalf("writeDefinition: %v", err)
	}
	got, err := readDefinition(dir)
	if err != nil {
		t.Fatalf("readDefinition: %v", err)
	}
	if !proto.Equal(got, sampleDefinition()) {
		t.Fatalf("expected skills in name order, got %v", got)
	}
}

func TestReadDefinitionAcceptsCRLFSkills(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skills", "triage", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\r\nname: triage\r\ndescription: Sort new issues\r\n---\r\n# Triage\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readDefinition(dir)
	if err != nil {
		t.Fatalf("readDefinition: %v", err)
	}
	want := []*agentv1.DepotAgentSkill{{Name: "triage", Description: "Sort new issues", Body: "# Triage\n"}}
	if len(got.GetSkills()) != 1 || !proto.Equal(got.GetSkills()[0], want[0]) {
		t.Fatalf("got %v", got.GetSkills())
	}
}

func TestPullRefusesToMixIntoADefinitionAlreadyThere(t *testing.T) {
	dir := t.TempDir()
	if err := writeDefinition(dir, sampleDefinition(), false); err != nil {
		t.Fatalf("writeDefinition: %v", err)
	}
	next := &agentv1.DepotAgentDefinitionContent{Skills: []*agentv1.DepotAgentSkill{{Name: "triage", Description: "d"}}}
	if err := writeDefinition(dir, next, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected a refusal naming --force, got %v", err)
	}
	if err := writeDefinition(dir, next, true); err != nil {
		t.Fatalf("writeDefinition --force: %v", err)
	}
	got, err := readDefinition(dir)
	if err != nil {
		t.Fatalf("readDefinition: %v", err)
	}
	if !proto.Equal(got, next) {
		t.Fatalf("--force should leave only the new definition, got %v", got)
	}
}

func TestPullRefusesASkillNameThatEscapesTheDirectory(t *testing.T) {
	dir := t.TempDir()
	bad := &agentv1.DepotAgentDefinitionContent{Skills: []*agentv1.DepotAgentSkill{{Name: "../escape", Description: "d"}}}
	if err := writeDefinition(dir, bad, false); err == nil {
		t.Fatal("expected a refusal")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); err == nil {
		t.Fatal("wrote outside the directory")
	}
}

func TestForcedPullWithABadSkillNameKeepsTheExistingDefinition(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, instructionsFile), "keep me")
	bad := &agentv1.DepotAgentDefinitionContent{Instructions: "new", Skills: []*agentv1.DepotAgentSkill{{Name: "../escape", Description: "d"}}}
	if err := writeDefinition(dir, bad, true); err == nil {
		t.Fatal("expected a refusal")
	}
	if data, err := os.ReadFile(filepath.Join(dir, instructionsFile)); err != nil || string(data) != "keep me" {
		t.Fatalf("existing definition was touched: %q, %v", data, err)
	}
}

func TestReadDefinitionRejectsBadSkills(t *testing.T) {
	for name, tc := range map[string]struct{ dir, body, want string }{
		"no front matter": {"a", "just text", "front matter"},
		"unclosed":        {"a", "---\ndescription: d\n", "closing ---"},
		"name mismatch":   {"a", "---\nname: b\ndescription: d\n---\n", `names "b"`},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, skillsDir, tc.dir, skillFile), tc.body)
			if _, err := readDefinition(dir); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestPushStoresADirectoryAndRollsBackToAVersion(t *testing.T) {
	f := &fakeAgentService{}
	s := startFake(t, f)
	ctx := context.Background()

	v1 := sampleDefinition()
	dir := filepath.Join(t.TempDir(), "def")
	if err := writeDefinition(dir, v1, false); err != nil {
		t.Fatalf("writeDefinition: %v", err)
	}
	if _, err := pushDefinition(ctx, s, "sess", dir, nil); err != nil {
		t.Fatalf("push v1: %v", err)
	}
	writeFile(t, filepath.Join(dir, instructionsFile), "Be thorough.\n")
	if resp, err := pushDefinition(ctx, s, "sess", dir, nil); err != nil || resp.GetDefinition().GetVersion() != 2 {
		t.Fatalf("push v2: %v %v", resp, err)
	}

	resp, err := pushDefinition(ctx, s, "sess", "", ptr(uint32(1)))
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if resp.GetDefinition().GetVersion() != 3 || !proto.Equal(f.definitions[2], v1) {
		t.Fatalf("rollback should store version 1's content as version 3, got %v", resp)
	}
}
