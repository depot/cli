//go:build !windows

package gocache

import "io"

// writeOutput writes an output file. On Unix, replacing a file does not affect
// readers that have it open, so outputs are written with writeAtomic.
func writeOutput(dest, _ string, _ int64, r io.Reader) (int64, error) {
	return writeAtomic(dest, r)
}
