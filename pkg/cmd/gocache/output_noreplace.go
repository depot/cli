package gocache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// outputLockName is the lock file, in each cache subdirectory, that serializes
// repairs of corrupt outputs.
const outputLockName = "depot-output.lock"

// noReplaceOps holds the platform operations that writeOutputNoReplace needs.
// They are parameters so that its logic is tested on every platform, while
// only Windows uses it.
type noReplaceOps struct {
	// renameNoReplace moves a file, and fails with an error matching
	// os.ErrExist when the destination exists.
	renameNoReplace func(from, to string) error
	// replace moves a file over an existing destination. It is only used to
	// repair a corrupt output while holding the lock.
	replace func(from, to string) error
	// lock takes an exclusive lock on path that is shared across processes.
	lock func(path string) (unlock func(), err error)
}

// writeOutputNoReplace writes the output with outputID to dest. Several actions
// can share one output, and other writers in this or another depot gocache
// process can write it at the same time. Once dest holds the output it is never
// replaced, because a reader may already be opening it.
func writeOutputNoReplace(dest, outputID string, size int64, r io.Reader, ops noReplaceOps) (int64, error) {
	if state, _ := checkOutput(dest, outputID, size); state == outputValid {
		refreshOutputTime(dest)
		return size, nil
	}

	tf, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".*")
	if err != nil {
		return 0, err
	}
	tmp := tf.Name()
	defer os.Remove(tmp)

	wrote, err := io.Copy(tf, r)
	if err != nil {
		tf.Close()
		return 0, err
	}
	if err := tf.Close(); err != nil {
		return 0, err
	}
	if wrote != size {
		// Never publish a short or long output: on Windows, a later writer could
		// only fix it with a repair.
		return wrote, fmt.Errorf("wrote %d bytes, expected %d", wrote, size)
	}

	err = ops.renameNoReplace(tmp, dest)
	if errors.Is(err, os.ErrExist) {
		err = keepOrRepairOutput(tmp, dest, outputID, size, ops)
	}
	if err != nil {
		return 0, err
	}
	return wrote, nil
}

// keepOrRepairOutput handles a dest that already exists. It keeps a dest that
// holds the output, and replaces it with tmp only when it is certainly corrupt.
// Repairs hold a lock and check dest again under it, so that a writer never
// replaces an output that another writer has already repaired and returned.
func keepOrRepairOutput(tmp, dest, outputID string, size int64, ops noReplaceOps) error {
	if state, _ := checkOutput(dest, outputID, size); state == outputValid {
		refreshOutputTime(dest)
		return nil
	}

	unlock, err := ops.lock(filepath.Join(filepath.Dir(dest), outputLockName))
	if err != nil {
		return fmt.Errorf("unable to lock %s for repair: %w", dest, err)
	}
	defer unlock()

	state, err := checkOutput(dest, outputID, size)
	switch state {
	case outputValid:
		refreshOutputTime(dest)
		return nil
	case outputCorrupt:
		return ops.replace(tmp, dest)
	case outputMissing:
		// Removed since the rename failed, for example by go clean.
		err = ops.renameNoReplace(tmp, dest)
		if errors.Is(err, os.ErrExist) {
			if state, _ = checkOutput(dest, outputID, size); state == outputValid {
				refreshOutputTime(dest)
				return nil
			}
		}
		return err
	default:
		// Never replace a file that could not be read, it may be valid.
		return fmt.Errorf("unable to check existing output %s: %w", dest, err)
	}
}

// refreshOutputTime marks dest, an output kept for a new action, as just
// written. The go command gets an entry's time from its output file's
// modification time, and uses it to expire test results and trim its cache, so
// a kept output must look as new as a rewritten one. This is best effort: a
// failure must not fail the put, and changing only the times does not conflict
// with open readers.
func refreshOutputTime(dest string) {
	_ = os.Chtimes(dest, time.Time{}, time.Now())
}

// outputState is what checkOutput found at an output's path.
type outputState int

const (
	outputUnknown outputState = iota
	outputMissing
	outputValid
	outputCorrupt
)

// checkOutput reports whether file holds the output with the given ID and
// size. The go command's output IDs are the SHA-256 of the content, so this is
// the same check that its own disk cache makes before reusing a file. A file
// is only outputCorrupt when it was read and does not match. When it cannot be
// read, the state is outputUnknown and err says why.
//
// Other GOCACHEPROG clients may use output IDs that are not a SHA-256. For
// those only the size can be checked, as DiskCache.Put does before writing. A
// client whose 64-hex IDs are not the SHA-256 of the content would have its
// outputs repaired on every put; the go command never does that.
func checkOutput(file, outputID string, size int64) (outputState, error) {
	want, err := hex.DecodeString(outputID)
	canHash := err == nil && len(want) == sha256.Size
	fi, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) {
		return outputMissing, nil
	}
	if err != nil {
		return outputUnknown, err
	}
	if !fi.Mode().IsRegular() || fi.Size() != size {
		return outputCorrupt, nil
	}
	if !canHash {
		return outputValid, nil
	}
	f, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return outputMissing, nil
	}
	if err != nil {
		return outputUnknown, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return outputUnknown, err
	}
	if n != size || !bytes.Equal(h.Sum(nil), want) {
		return outputCorrupt, nil
	}
	return outputValid, nil
}
