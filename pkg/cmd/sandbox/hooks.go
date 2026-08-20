package sandbox

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	sandboxv1 "github.com/depot/cli/pkg/proto/depot/sandbox/v1"
	"github.com/depot/cli/pkg/proto/depot/sandbox/v1/sandboxv1connect"
	"github.com/depot/cli/pkg/sandbox"
	"github.com/spf13/cobra"
)

// addHookFlags declares the --file, --set, and --no-hook flags that every
// hook-aware command shares. stageLabel (such as "on.exec" or "on.shell") is
// interpolated into the help text.
func addHookFlags(cmd *cobra.Command, stageLabel string) {
	cmd.Flags().StringP("file", "f", "", fmt.Sprintf("Path to a trusted sandbox.depot.yml file for %s hooks", stageLabel))
	cmd.Flags().StringArray("set", nil, fmt.Sprintf("Inputs as KEY=VALUE for %s ${input.KEY} substitution; repeatable", stageLabel))
	cmd.Flags().Bool("no-hook", false, fmt.Sprintf("Skip %s hooks declared in the spec", stageLabel))
}

// runHookStage reads the --file, --set, and --no-hook flags, resolves the named
// stage from the local spec, and runs its hooks against sandboxID. The --no-hook
// flag short-circuits to a no-op. pick selects which stage's hooks to resolve.
func runHookStage(
	ctx context.Context,
	cmd *cobra.Command,
	client sandboxv1connect.SandboxServiceClient,
	token, orgID, sandboxID, label string,
	stage sandboxv1.HookStage,
	pick func(*sandbox.Spec) []sandbox.HookSpec,
	stdout, stderr io.Writer,
) error {
	noHook, _ := cmd.Flags().GetBool("no-hook")
	if noHook {
		return nil
	}
	file, _ := cmd.Flags().GetString("file")
	setPairs, _ := cmd.Flags().GetStringArray("set")
	if file == "" {
		if len(setPairs) > 0 {
			return fmt.Errorf("%s --set requires --file", label)
		}
		return nil
	}
	hooks, err := resolveStageHooks(file, label, setPairs, pick)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", label, err)
	}
	return runHooks(ctx, client, token, orgID, sandboxID, label, stage, hooks, stdout, stderr)
}

// resolveStageHooks loads an explicitly selected sandbox.depot.yml and resolves
// only the requested hook stage.
func resolveStageHooks(file, label string, setPairs []string, pick func(*sandbox.Spec) []sandbox.HookSpec) ([]sandbox.HookSpec, error) {
	if file == "" {
		return nil, nil
	}
	spec, err := sandbox.Load(file)
	if err != nil {
		return nil, err
	}
	inputs, err := sandbox.ParseInputs(setPairs)
	if err != nil {
		return nil, err
	}
	return spec.ResolveHookStage(label, pick(spec), inputs)
}

// runHooks runs each hook in turn against the given sandbox. label identifies
// the stage in CLI output (such as "on.exec" or "on.down"). A non-zero exit from
// any foreground hook aborts and returns immediately. A detached hook fails only
// if the spawn itself fails.
func runHooks(ctx context.Context, client sandboxv1connect.SandboxServiceClient, token, orgID, sandboxID, label string, stage sandboxv1.HookStage, hooks []sandbox.HookSpec, stdout, stderr io.Writer) error {
	if len(hooks) == 0 {
		return nil
	}
	if sandboxID == "" {
		return fmt.Errorf("%s: missing sandbox id", label)
	}

	for i, h := range hooks {
		name := hookDisplayName(h, i)
		fmt.Fprintf(stdout, "[%s %s]\n", label, name)
		if err := runHook(ctx, client, token, orgID, sandboxID, name, stage, h, stdout, stderr); err != nil {
			return fmt.Errorf("%s[%s]: %w", label, name, err)
		}
	}
	return nil
}

func runHook(ctx context.Context, client sandboxv1connect.SandboxServiceClient, token, orgID, sandboxID, name string, stage sandboxv1.HookStage, h sandbox.HookSpec, stdout, stderr io.Writer) error {
	hook := &sandboxv1.HookSpec{
		Command: h.Command,
	}
	if h.Detach {
		hook.Detach = &h.Detach
	}
	if h.Name != "" {
		hook.Name = &h.Name
	}
	if h.TimeoutSeconds > 0 {
		timeout := int32(h.TimeoutSeconds)
		hook.TimeoutSeconds = &timeout
	}

	req := &sandboxv1.RunHookRequest{
		Sandbox: sandboxRef(sandboxID),
		Stage:   stage,
		Hook:    hook,
	}

	stream, err := client.RunHook(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return fmt.Errorf("hook: %w", err)
	}

	exit, err := consumeCommandEventStream(stream, stdout, stderr)
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("exit %d", exit)
	}
	if h.Detach {
		fmt.Fprintf(stdout, "[%s detached]\n", name)
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
