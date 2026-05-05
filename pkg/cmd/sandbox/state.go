package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
)

// We track the most recent sandbox_id per spec name in
// ~/.depot/sandbox-state/<sanitized-name>.id so `depot sandbox up` can refuse
// to launch a duplicate while one is already alive. This is a pure local
// convenience — the API doesn't yet carry a spec name (sandbox.proto:Sandbox
// has no name field), so cross-machine collisions still slip through.
//
// TODO(DEP-XXXX): replace with a server-side name index once the proto adds
// a name field; until then anyone running `depot sandbox up` from a second
// machine won't see the first machine's record.

func sandboxStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".depot", "sandbox-state"), nil
}

func sandboxStatePath(name string) (string, error) {
	dir, err := sandboxStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sanitizeStateName(name)+".id"), nil
}

// sanitizeStateName keeps only safe characters so a spec name with slashes
// or dots can't escape the state dir.
func sanitizeStateName(name string) string {
	if name == "" {
		return "_unnamed"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, name)
}

func loadSandboxState(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	path, err := sandboxStatePath(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func saveSandboxState(name, sandboxID string) error {
	if name == "" {
		return nil
	}
	path, err := sandboxStatePath(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(sandboxID+"\n"), 0600)
}

// assertNoLiveSandbox returns an error if a sandbox previously launched under
// `name` is still running. A stale state file (sandbox already terminated, or
// killed via depot UI) is silently ignored — we just return nil and let the
// caller proceed; `up` overwrites the file on success.
func assertNoLiveSandbox(ctx context.Context, client agentv1connect.SandboxServiceClient, token, orgID, name string) error {
	prevID, err := loadSandboxState(name)
	if err != nil || prevID == "" {
		return nil
	}
	res, err := client.GetSandbox(ctx, api.WithAuthenticationAndOrg(
		connect.NewRequest(&agentv1.GetSandboxRequest{SandboxId: prevID}), token, orgID))
	if err != nil {
		// 404 / NotFound — sandbox no longer exists, which is the
		// common "stale state" path. Other errors (auth / network) we
		// surface so callers don't accidentally launch a duplicate
		// while the API is unreachable.
		if connect.CodeOf(err) == connect.CodeNotFound {
			return nil
		}
		return fmt.Errorf("check existing sandbox %s: %w", prevID, err)
	}
	if res.Msg.Sandbox == nil || res.Msg.Sandbox.CompletedAt != nil {
		return nil
	}
	return fmt.Errorf("sandbox %q is already running as %s; kill it first with `depot sandbox kill %s`",
		name, prevID, prevID)
}
