package packages

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/cli/safeexec"
)

// systemWriteScript writes stdin to a file through a unique temporary file in
// the same directory, so readers and concurrent writers never see a partial
// file. It writes through a symbolic link to its target, so a link to a
// managed file stays in place, and refuses a target that isn't a regular file.
const systemWriteScript = `set -e
dest="$1"
if [ -L "$dest" ]; then dest=$(readlink -f "$dest"); fi
if [ -e "$dest" ] && [ ! -f "$dest" ]; then echo "$dest isn't a regular file" >&2; exit 1; fi
mkdir -p "$(dirname "$dest")"
tmp=$(mktemp "$dest.depot-tmp.XXXXXX")
trap 'rm -f "$tmp"' EXIT
cat > "$tmp"
chmod "$2" "$tmp"
mv -f "$tmp" "$dest"`

func (s *setup) requireSystemClient() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("%s setup is only supported on Linux", s.clientName)
	}
	return nil
}

func (s *setup) privilegedCommand(name string, args ...string) (*exec.Cmd, error) {
	if os.Geteuid() == 0 {
		return exec.CommandContext(s.ctx, name, args...), nil
	}
	for _, elevate := range []string{"sudo", "doas"} {
		if path, err := safeexec.LookPath(elevate); err == nil {
			if !s.elevationNoticeShown {
				fmt.Fprintf(os.Stderr, "Updating system configuration with %s.\n", elevate)
				s.elevationNoticeShown = true
			}
			return exec.CommandContext(s.ctx, path, append([]string{name}, args...)...), nil
		}
	}
	return nil, fmt.Errorf("%s setup changes system files: rerun as root, or install sudo or doas", s.clientName)
}

func (s *setup) runPrivileged(input []byte, name string, args ...string) ([]byte, error) {
	cmd, err := s.privilegedCommand(name, args...)
	if err != nil {
		return nil, err
	}
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w\n%s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (s *setup) writeSystemFile(path string, data []byte, perm os.FileMode) error {
	if _, err := s.runPrivileged(data, "sh", "-c", systemWriteScript, "sh", path, fmt.Sprintf("%04o", perm)); err != nil {
		return err
	}
	s.changedFile(path)
	return nil
}

func (s *setup) readSystemFile(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		return data, nil
	} else if os.IsNotExist(err) {
		return nil, nil
	}
	return s.runPrivileged(nil, "sh", "-c", `if [ -e "$1" ]; then cat "$1"; fi`, "sh", path)
}

func (s *setup) download(url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("depot", s.token)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("could not download %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func osRelease() map[string]string {
	values := map[string]string{}
	file, err := os.Open("/etc/os-release")
	if err != nil {
		return values
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), "=")
		if found {
			values[key] = strings.Trim(value, `"'`)
		}
	}
	if scanner.Err() != nil {
		return map[string]string{}
	}
	return values
}

// authMachine returns the machine name APT and netrc use. APT applies an
// entry without a scheme to HTTPS only, so plain HTTP needs the scheme.
func (r repository) authMachine(path string) string {
	machine := r.host() + path
	if r.server.Scheme == "http" {
		machine = "http://" + machine
	}
	return machine
}

func (r repository) resourcePath() string {
	return r.server.EscapedPath() + "/" + r.org + "/" + r.format + "/" + r.name + "/"
}

// fileID names system files for the repository. Repository names can't
// contain underscores, so the underscore separator keeps IDs unique.
func (r repository) fileID() string {
	return "depot-" + r.org + "_" + r.name
}
