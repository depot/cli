package packages

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func readFileIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// writeFile replaces the file atomically. It writes through symbolic links so
// that configuration files managed from a dotfiles directory stay linked.
func (s *setup) writeFile(path string, data []byte, perm os.FileMode) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
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
