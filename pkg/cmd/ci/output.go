package ci

import (
	"encoding/json"
	"fmt"
	"os"
)

const (
	outputFormatText = "text"
	outputFormatJSON = "json"
)

// writeJSON encodes v as indented JSON to stdout. Used by the `--output json`
// path on `depot ci` verbs so every verb formats RPC responses identically.
func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func validateTextOrJSONOutput(output string) error {
	if output == "" || output == outputFormatText || output == outputFormatJSON {
		return nil
	}
	return fmt.Errorf("unsupported output %q (valid: text, json)", output)
}

func outputIsJSON(output string) bool {
	return output == outputFormatJSON
}
