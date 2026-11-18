package gocache

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// writeOutput writes an output file without replacing an output that is
// already on disk, see writeOutputNoReplace. On Windows, replacing a file makes
// a concurrent open of it, such as by vet.exe, fail with a sharing violation.
func writeOutput(dest, outputID string, size int64, r io.Reader) (int64, error) {
	return writeOutputNoReplace(dest, outputID, size, r, noReplaceOps{
		renameNoReplace: moveFileNoReplace,
		replace:         replaceFile,
		lock:            lockFile,
	})
}

// moveFileNoReplace moves from to to, and fails with an error matching
// os.ErrExist when to already exists. It calls MoveFileExW without
// MOVEFILE_REPLACE_EXISTING, which leaves an existing to untouched.
func moveFileNoReplace(from, to string) error {
	fromPtr, err := extendedPathPtr(from)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	toPtr, err := extendedPathPtr(to)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	if err := windows.MoveFileEx(fromPtr, toPtr, 0); err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}

// extendedPathPtr returns path in its extended-length (\\?\) form, so that
// MoveFileExW accepts paths longer than MAX_PATH even when long paths are not
// enabled on the system. The os package does the same for os.Rename, but does
// not export it.
func extendedPathPtr(path string) (*uint16, error) {
	return windows.UTF16PtrFromString(extendedPath(path))
}

func extendedPath(path string) string {
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\??\`) || strings.HasPrefix(path, `\\.\`) {
		// Already extended, or a device path whose meaning a prefix would change.
		return path
	}
	// The \\?\ prefix turns off path normalization, so the path must be
	// absolute, cleaned and use backslashes. filepath.Abs does all three.
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if strings.HasPrefix(abs, `\\`) {
		// UNC path: \\server\share\... becomes \\?\UNC\server\share\...
		return `\\?\UNC\` + abs[2:]
	}
	return `\\?\` + abs
}

// replaceTimeout bounds how long replaceFile waits for other handles on the
// destination to close. It matches the go command's robustio package.
const replaceTimeout = 2 * time.Second

// replaceFile moves from over an existing to. MoveFileExW with
// MOVEFILE_REPLACE_EXISTING fails while any other handle has to open, even one
// that allows FILE_SHARE_DELETE. Other depot writers open the output briefly
// and without the lock to hash it, so like the go command's robustio package,
// replaceFile retries until they let go.
func replaceFile(from, to string) error {
	start := time.Now()
	backoff := time.Millisecond
	for {
		err := os.Rename(from, to)
		if err == nil || !isSharingError(err) || time.Since(start)+backoff > replaceTimeout {
			return err
		}
		time.Sleep(backoff)
		backoff = min(2*backoff, 100*time.Millisecond)
	}
}

// isSharingError reports whether err is from a file that another handle has
// open.
func isSharingError(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

// lockFile takes an exclusive LockFileEx lock on path, blocking until it is
// free. Windows releases the lock if the process exits without unlocking.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		f.Close()
		return nil, &os.PathError{Op: "lock", Path: path, Err: err}
	}
	return func() {
		_ = windows.UnlockFileEx(h, 0, 1, 0, ol)
		f.Close()
	}, nil
}
