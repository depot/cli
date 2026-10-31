package packages

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// readFileIfExists rejects a path that isn't a regular file, because reading
// a FIFO or device would block or return unrelated data.
func readFileIfExists(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("could not read %s: it isn't a regular file", path)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// resolveLink follows symbolic links to the final path, including a target
// that doesn't exist yet, so that a write replaces the target and not the link.
func resolveLink(path string) string {
	for range 40 {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return path
		}
		target, err := os.Readlink(path)
		if err != nil {
			return path
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}
	return path
}

// restrictPermissions limits a client's configuration file to its owner after
// the client has saved the token in it.
func restrictPermissions(path string) error {
	err := os.Chmod(resolveLink(path), 0600)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// writeFile replaces the file atomically. It writes through symbolic links so
// that configuration files managed from a dotfiles directory stay linked.
func (s *setup) writeFile(path string, data []byte, perm os.FileMode) error {
	path = resolveLink(path)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("could not update %s: it isn't a regular file", path)
	}

	existing, err := readFileIfExists(path)
	if err != nil {
		return err
	}
	if existing != nil && string(existing) == string(data) {
		if info, err := os.Stat(path); err == nil && info.Mode().Perm() == perm {
			return nil
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}

	s.changedFile(path)
	return nil
}

// configFileMode keeps the permissions of an existing file and uses perm for
// a new file.
func configFileMode(path string, perm os.FileMode) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	return perm
}
