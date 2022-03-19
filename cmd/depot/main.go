package main

import (
	"os"

	"github.com/depot/cli/internal/build"
	"github.com/depot/cli/pkg/cmd/root"
)

func main() {
	code := runMain()
	os.Exit(code)
}

func runMain() int {
	buildVersion := build.Version
	buildDate := build.Date

	rootCmd := root.NewCmdRoot(buildVersion, buildDate)

	if err := rootCmd.Execute(); err != nil {
		return 1
	}

	return 0
}
