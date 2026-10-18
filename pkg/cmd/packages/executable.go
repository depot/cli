package packages

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cli/safeexec"
)

const tokenHelperName = "depot-package-token"

// depotExecutable prefers the depot found on PATH when it is the running
// binary, because PATH entries such as Homebrew's bin directory stay the same
// across upgrades while the resolved executable path can change.
func depotExecutable() (path string, stable bool, err error) {
	self, err := os.Executable()
	if err != nil {
		return "", false, fmt.Errorf("could not find the depot executable: %w", err)
	}
	self, err = filepath.Abs(self)
	if err != nil {
		return "", false, err
	}

	found, err := safeexec.LookPath("depot")
	if err != nil {
		return self, false, nil
	}
	found, err = filepath.Abs(found)
	if err != nil {
		return self, false, nil
	}

	if sameFile(found, self) {
		return found, true, nil
	}
	return self, false, nil
}

func sameFile(a, b string) bool {
	aInfo, err := os.Stat(a)
	if err != nil {
		return false
	}
	bInfo, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(aInfo, bInfo)
}

func depotBinDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".depot", "bin"), nil
}

// installTokenHelper writes the wrapper that clients without argument support,
// such as pnpm, run to obtain a token.
func (s *setup) installTokenHelper() (string, error) {
	depot, stable, err := depotExecutable()
	if err != nil {
		return "", err
	}
	if !stable {
		s.notef("The token helper runs %s. Rerun this command if that path changes, for example after an upgrade.", depot)
	}

	dir, err := depotBinDir()
	if err != nil {
		return "", err
	}

	path := filepath.Join(dir, tokenHelperName)
	var script string
	if runtime.GOOS == "windows" {
		path += ".cmd"
		script = "@echo off\r\nset DEPOT_NO_UPDATE_NOTIFIER=1\r\n\"" + depot + "\" packages token\r\n"
	} else {
		script = "#!/bin/sh\nDEPOT_NO_UPDATE_NOTIFIER=1 exec " + shellQuote(depot) + " packages token\n"
	}

	if err := s.writeFile(path, []byte(script), 0755); err != nil {
		return "", err
	}
	return path, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
