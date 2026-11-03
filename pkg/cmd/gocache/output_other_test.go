//go:build !windows

package gocache

// platformNoReplaceOps is empty: only Windows has its own implementation.
var platformNoReplaceOps = map[string]noReplaceOps{}
