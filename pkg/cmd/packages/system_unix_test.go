//go:build !windows

package packages

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadSystemFileRejectsSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0600))
	s := newTestSetup(t, "npm", "npmjs")
	_, err := s.readSystemFile(fifo)
	require.ErrorContains(t, err, "isn't a regular file")

	data, err := s.readSystemFile(filepath.Join(dir, "missing"))
	require.NoError(t, err)
	require.Nil(t, data)
}
