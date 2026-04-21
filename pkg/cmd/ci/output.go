package ci

import (
	"encoding/json"
	"os"
)

// writeJSON encodes v as indented JSON to stdout. Used by the `--output json`
// path on `depot ci` verbs so every verb formats RPC responses identically.
func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
