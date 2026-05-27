package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
	"github.com/depot/cli/pkg/proto/depot/sandbox/v1/sandboxv1connect"
	"github.com/depot/cli/pkg/sandbox"
	"github.com/spf13/cobra"
)

// hookWrapperScript is the shell stub that runs every CLI-side hook command
// (on.exec / on.shell / on.snapshot / on.down). The on.create / on.start
// stages run server-side now via CreateSandboxFromSpec's HookEvents (M34 /
// D-M34-I) so the wrapper here only covers the four stages that still wrap
// around the user's command on the client.
//
// Positional args (filled in by hook caller):
//
//	$0 = "depot-hook" (script-name placeholder)
//	$1 = the user's hook command (single string, run via bash -lc)
//	$2 = log file path (used only when detached)
//	$3 = "1" if the hook should be detached, "0" otherwise
//
// /tmp/.depot-env is sourced when present so detached processes inherit the
// same env that the API's exec wrapper would normally set up.
const hookWrapperScript = `[ -f /tmp/.depot-env ] && . /tmp/.depot-env
if [ "$3" = "1" ]; then
  mkdir -p "$(dirname "$2")"
  setsid nohup /bin/bash -lc "$1" </dev/null >>"$2" 2>&1 &
  exit 0
fi
exec /bin/bash -lc "$1"
`

// addHookFlags declares the --file / --set / --no-hook triple every hook-aware
// command shares. stageLabel ("on.exec", "on.shell", …) is interpolated into
// the help text so `--help` reads naturally.
func addHookFlags(cmd *cobra.Command, stageLabel string) {
	cmd.Flags().StringP("file", "f", "", fmt.Sprintf("Path to a sandbox.depot.yml file for %s resolution (default: walk up from cwd)", stageLabel))
	cmd.Flags().StringArray("set", nil, fmt.Sprintf("Inputs as KEY=VALUE for %s ${input.KEY} substitution; repeatable", stageLabel))
	cmd.Flags().Bool("no-hook", false, fmt.Sprintf("Skip %s hooks declared in the spec", stageLabel))
}

// runHookStage reads the standard --file/--set/--no-hook flags, resolves the
// named stage from the local spec, and runs it against sandboxID. A --no-hook
// flag short-circuits to a no-op. pick selects which stage's hooks to fire
// out of the resolved HooksSpec.
//
// Sessions are gone in v0 (D-M34-M) — RunCommand on sandboxv1 takes a
// SandboxRef only.
func runHookStage(
	ctx context.Context,
	cmd *cobra.Command,
	client sandboxv1connect.SandboxServiceClient,
	token, orgID, sandboxID, label string,
	pick func(sandbox.HooksSpec) []sandbox.HookSpec,
	stdout, stderr io.Writer,
) error {
	noHook, _ := cmd.Flags().GetBool("no-hook")
	if noHook {
		return nil
	}
	file, _ := cmd.Flags().GetString("file")
	setPairs, _ := cmd.Flags().GetStringArray("set")
	hooks, err := resolveStageHooks(file, setPairs)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", label, err)
	}
	return runHooks(ctx, client, token, orgID, sandboxID, label, pick(hooks), stdout, stderr)
}

// resolveStageHooks loads the nearest sandbox.depot.yml (walking up from
// cwd, or honoring --file if non-empty) and returns the resolved hooks for
// every stage. Returns a zero-valued HooksSpec if no spec is found — that
// matches the behavior of `exec`/`shell`/`snapshot` against a raw sandbox
// id outside any project tree.
func resolveStageHooks(file string, setPairs []string) (sandbox.HooksSpec, error) {
	var path string
	if file != "" {
		path = file
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return sandbox.HooksSpec{}, err
		}
		p, err := sandbox.FindSpec(cwd)
		if err != nil {
			return sandbox.HooksSpec{}, nil //nolint:nilerr // no spec, no hooks
		}
		path = p
	}
	spec, err := sandbox.Load(path)
	if err != nil {
		return sandbox.HooksSpec{}, err
	}
	inputs, err := sandbox.ParseInputs(setPairs)
	if err != nil {
		return sandbox.HooksSpec{}, err
	}
	return spec.ResolveHooks(inputs)
}

// runHooks runs each hook sequentially against the given sandbox via
// RunCommand on the sandboxv1 wire. label is used in CLI output
// ("on.exec" / "on.shell" / "on.snapshot" / "on.down"). A non-zero exit
// from any foreground hook aborts and returns immediately. Detached hooks
// only fail if the spawn itself fails.
func runHooks(ctx context.Context, client sandboxv1connect.SandboxServiceClient, token, orgID, sandboxID, label string, hooks []sandbox.HookSpec, stdout, stderr io.Writer) error {
	if len(hooks) == 0 {
		return nil
	}
	if sandboxID == "" {
		return fmt.Errorf("%s: missing sandbox id", label)
	}

	for i, h := range hooks {
		name := hookDisplayName(h, i)
		fmt.Fprintf(stdout, "[%s %s] %s\n", label, name, h.Command)
		if err := runHook(ctx, client, token, orgID, sandboxID, name, h, stdout, stderr); err != nil {
			return fmt.Errorf("%s[%s]: %w", label, name, err)
		}
	}
	return nil
}

func runHook(ctx context.Context, client sandboxv1connect.SandboxServiceClient, token, orgID, sandboxID, name string, h sandbox.HookSpec, stdout, stderr io.Writer) error {
	logFile := fmt.Sprintf("/tmp/depot-hook-%s.log", name)
	detachFlag := "0"
	if h.Detach {
		detachFlag = "1"
	}

	req := &sandboxv1.RunCommandRequest{
		Sandbox: sandboxRef(sandboxID),
		Cmd:     "/bin/sh",
		Args:    []string{"-c", hookWrapperScript, "depot-hook", h.Command, logFile, detachFlag},
	}

	stream, err := client.RunCommand(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}

	exit, err := consumeCommandEventStream(stream, stdout, stderr)
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("exit %d", exit)
	}
	if h.Detach {
		fmt.Fprintf(stdout, "[%s detached, log: %s]\n", name, logFile)
	}
	return nil
}

// hookNameSanitize keeps log paths predictable and safe for shell.
var hookNameSanitize = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func hookDisplayName(h sandbox.HookSpec, idx int) string {
	if h.Name != "" {
		clean := hookNameSanitize.ReplaceAllString(h.Name, "_")
		clean = strings.Trim(clean, "_")
		if clean != "" {
			return clean
		}
	}
	return fmt.Sprintf("%d", idx)
}
