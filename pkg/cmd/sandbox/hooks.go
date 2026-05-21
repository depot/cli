package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/proto/depot/ci/v1/civ1connect"
	"github.com/depot/cli/pkg/sandbox"
)

// hookWrapperScript is the shell stub that runs every hook command. Passing
// the user's command via $1 (positional) instead of interpolating into the
// script itself avoids quote/escape pitfalls on the YAML side.
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

// joinShellHooks renders on.shell entries into a single shell line ready
// to be sent over the pty as if the user had typed it. We chain with `;`
// instead of `&&` so a non-zero exit from the first entry doesn't gate the
// rest — the on.shell stage's job is to land the user *somewhere*; if the
// last entry exec's into tmux/whatever, prior failures don't matter.
// Each line ends with `\n` so the pty's line discipline forwards it to
// the shell's stdin buffer immediately.
func joinShellHooks(hooks []sandbox.HookSpec) string {
	if len(hooks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(hooks))
	for _, h := range hooks {
		c := strings.TrimSpace(h.Command)
		if c == "" {
			continue
		}
		parts = append(parts, c)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + "\n"
}

// runHooks runs each hook sequentially against the given sandbox/session.
// label is used in CLI output ("on_create" / "on_start"). A non-zero exit
// from any foreground hook aborts and returns immediately. Detached hooks
// only fail if the spawn itself fails.
func runHooks(ctx context.Context, client civ1connect.DepotComputeServiceClient, token, orgID, sandboxID, sessionID, label string, hooks []sandbox.HookSpec, stdout, stderr io.Writer) error {
	if len(hooks) == 0 {
		return nil
	}
	if sandboxID == "" || sessionID == "" {
		return fmt.Errorf("%s: missing sandbox/session id (got sandbox=%q session=%q)", label, sandboxID, sessionID)
	}

	for i, h := range hooks {
		name := hookDisplayName(h, i)
		fmt.Fprintf(stdout, "[%s %s] %s\n", label, name, h.Command)
		if err := runHook(ctx, client, token, orgID, sandboxID, sessionID, name, h, stdout, stderr); err != nil {
			return fmt.Errorf("%s[%s]: %w", label, name, err)
		}
	}
	return nil
}

func runHook(ctx context.Context, client civ1connect.DepotComputeServiceClient, token, orgID, sandboxID, sessionID, name string, h sandbox.HookSpec, stdout, stderr io.Writer) error {
	logFile := fmt.Sprintf("/tmp/depot-hook-%s.log", name)
	detachFlag := "0"
	timeout := int32(h.TimeoutSeconds * 1000)
	if h.Detach {
		detachFlag = "1"
		// The wrapper exits as soon as the daemon is spawned, so a
		// short ceiling protects against a stuck setsid call without
		// killing the daemon itself.
		timeout = 30 * 1000
	}

	req := &civ1.ExecuteCommandRequest{
		SandboxId: sandboxID,
		SessionId: sessionID,
		Command: &civ1.Command{
			CommandArray: []string{"/bin/sh", "-c", hookWrapperScript, "depot-hook", h.Command, logFile, detachFlag},
			TimeoutMs:    timeout,
		},
	}

	stream, err := client.RemoteExec(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}

	exit, err := consumeRemoteExec(stream, stdout, stderr)
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

// resolveSession fetches the session id for a sandbox so we can RemoteExec
// against it. StartSandbox returns the session id directly, so most callers
// don't need this; it's here for `exec`/`shell` against an existing sandbox.
//
// Retries with a short backoff to ride out the lag between StartSandbox
// returning and GetSandbox seeing the freshly-allocated session — that
// otherwise surfaces as a "session not found" error when a script chains
// `depot sandbox up && depot sandbox exec ...` immediately.
func resolveSession(ctx context.Context, sandboxID, token, orgID string) (string, error) {
	if sandboxID == "" {
		return "", fmt.Errorf("sandbox id required")
	}
	sb := api.NewSandboxClient()
	var lastErr error
	for _, delay := range []time.Duration{0, 500 * time.Millisecond, 1 * time.Second, 2 * time.Second} {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
			}
		}
		res, err := sb.GetSandbox(ctx, api.WithAuthenticationAndOrg(
			connect.NewRequest(&agentv1.GetSandboxRequest{SandboxId: sandboxID}), token, orgID))
		if err != nil {
			lastErr = fmt.Errorf("get sandbox: %w", err)
			continue
		}
		if res.Msg.Sandbox == nil {
			lastErr = fmt.Errorf("sandbox %s not found", sandboxID)
			continue
		}
		if res.Msg.Sandbox.SessionId == "" {
			lastErr = fmt.Errorf("sandbox %s has no session yet", sandboxID)
			continue
		}
		return res.Msg.Sandbox.SessionId, nil
	}
	return "", lastErr
}
