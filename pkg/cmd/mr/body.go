package mr

import (
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

// bodyFlags is the --body and --body-file pair shared by create, edit and comment.
type bodyFlags struct{ text, file string }

func (b *bodyFlags) register(cmd *cobra.Command, usage string) {
	cmd.Flags().StringVar(&b.text, "body", "", usage)
	cmd.Flags().StringVar(&b.file, "body-file", "", `Read the body from a file ("-" reads standard input)`)
	cmd.MarkFlagsMutuallyExclusive("body", "body-file")
}

// read returns the body and whether either flag was passed.
// File contents are sent unchanged, including any trailing newline.
func (b *bodyFlags) read(cmd *cobra.Command) (string, bool, error) {
	if !cmd.Flags().Changed("body-file") {
		return b.text, cmd.Flags().Changed("body"), nil
	}
	var data []byte
	var err error
	if b.file == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(b.file)
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to read --body-file %q: %w", b.file, err)
	}
	if !utf8.Valid(data) {
		return "", false, fmt.Errorf("--body-file %q is not valid UTF-8 text", b.file)
	}
	return string(data), true, nil
}
