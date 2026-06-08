package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/depot/cli/pkg/sandbox"
)

// Confirms every verb is registered on the root command. This catches the
// wiring mistake where a verb file exists but sandbox.go forgets to call
// AddCommand for it.
func TestSandboxVerbTree(t *testing.T) {
	root := NewCmdSandbox()

	want := []string{"create", "exec", "stop", "kill"}
	got := map[string]bool{}
	for _, c := range root.Commands() {
		// A cobra Use string can include a "[flags] <args...>" suffix; the
		// first whitespace-separated token is the verb name.
		name := c.Use
		if idx := strings.IndexAny(name, " \t"); idx > 0 {
			name = name[:idx]
		}
		got[name] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing verb %q on `depot sandbox`", w)
		}
	}
}

func TestSandboxStopRequiresExplicitID(t *testing.T) {
	cmd := newSandboxStop()
	if err := cmd.Args(cmd, nil); err == nil {
		t.Fatal("expected stop without ids to fail argument validation")
	}
}

func TestSandboxKillRequiresExplicitID(t *testing.T) {
	cmd := newSandboxKill()
	if err := cmd.Args(cmd, nil); err == nil {
		t.Fatal("expected kill without ids to fail argument validation")
	}
}

func TestRunHookStageDoesNotDiscoverSpecImplicitly(t *testing.T) {
	cmd := newSandboxExec()
	if err := runHookStage(context.Background(), cmd, nil, "", "", "cs-123", "on.exec", 0, nil, nil, nil); err != nil {
		t.Fatalf("runHookStage without --file returned error: %v", err)
	}
}

func TestRunHookStageSetRequiresFile(t *testing.T) {
	cmd := newSandboxExec()
	if err := cmd.Flags().Set("set", "name=value"); err != nil {
		t.Fatalf("set flag: %v", err)
	}

	err := runHookStage(context.Background(), cmd, nil, "", "", "cs-123", "on.exec", 0, nil, nil, nil)
	if err == nil {
		t.Fatal("expected --set without --file to fail")
	}
	if !strings.Contains(err.Error(), "--set requires --file") {
		t.Fatalf("error = %q, want --set requires --file", err)
	}
}

func TestResolveStageHooksOnlyValidatesSelectedStage(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "sandbox.depot.yml")
	if err := os.WriteFile(specPath, []byte(`
on:
  exec:
    - echo ${input.name}
  down:
    - ""
`), 0644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	hooks, err := resolveStageHooks(specPath, "on.exec", []string{"name=value"}, func(s *sandbox.Spec) []sandbox.HookSpec {
		return s.On.Exec
	})
	if err != nil {
		t.Fatalf("resolveStageHooks returned error: %v", err)
	}
	if len(hooks) != 1 || hooks[0].Command != "echo 'value'" {
		t.Fatalf("hooks = %#v, want resolved on.exec only", hooks)
	}
}

func TestSandboxStopSetRequiresFile(t *testing.T) {
	cmd := newSandboxStop()
	cmd.SetArgs([]string{"cs-123", "--set", "name=value"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected stop --set without --file to fail")
	}
	if !strings.Contains(err.Error(), "--set requires --file") {
		t.Fatalf("error = %q, want --set requires --file", err)
	}
}

func TestSandboxExecAcceptsLegacySandboxIDFlag(t *testing.T) {
	cmd := newSandboxExec()
	if err := cmd.Flags().Set("sandbox-id", "cs-legacy"); err != nil {
		t.Fatalf("set sandbox-id: %v", err)
	}
	if err := cmd.Flags().Set("session-id", "session-legacy"); err != nil {
		t.Fatalf("set session-id: %v", err)
	}

	sandboxID, cmdArgs, err := sandboxExecTarget(cmd, []string{"/bin/echo", "hello"})
	if err != nil {
		t.Fatalf("sandboxExecTarget returned error: %v", err)
	}
	if sandboxID != "cs-legacy" {
		t.Fatalf("sandboxID = %q, want cs-legacy", sandboxID)
	}
	if strings.Join(cmdArgs, " ") != "/bin/echo hello" {
		t.Fatalf("cmdArgs = %q, want /bin/echo hello", cmdArgs)
	}
}

func TestSandboxExecAcceptsPositionalSandboxID(t *testing.T) {
	cmd := newSandboxExec()

	sandboxID, cmdArgs, err := sandboxExecTarget(cmd, []string{"cs-positional", "/bin/echo", "hello"})
	if err != nil {
		t.Fatalf("sandboxExecTarget returned error: %v", err)
	}
	if sandboxID != "cs-positional" {
		t.Fatalf("sandboxID = %q, want cs-positional", sandboxID)
	}
	if strings.Join(cmdArgs, " ") != "/bin/echo hello" {
		t.Fatalf("cmdArgs = %q, want /bin/echo hello", cmdArgs)
	}
}
