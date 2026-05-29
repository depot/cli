package sandbox

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// We track the most recent sandbox_id per spec name in
// ~/.depot/sandbox-state/<sanitized-name>.id so `depot sandbox from-spec` can
// refuse to launch a duplicate while one is already alive. This is a pure
// local convenience: the sandbox's create-time label is now returned on reads,
// but the API still has no server-side registry that maps a spec name to its
// live sandbox, so cross-machine collisions slip through. Once that exists,
// this lookup should move server-side.

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
