package helpers

import (
	"os"

	"github.com/depot/cli/pkg/ci"
	"github.com/mattn/go-isatty"
)

func IsTerminal() bool {
	_, isCI := ci.Provider()
	return !isCI && isTerminal(os.Stdout) && isTerminal(os.Stderr)
}

func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
