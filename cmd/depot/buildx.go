package main

import (
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"

	"github.com/docker/cli/cli/config"
	"github.com/pkg/errors"
)

func buildxMain() error {
	args := os.Args[1:]
	cmd := ""
	subcmd := ""

	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			if cmd == "" {
				cmd = arg
			} else if subcmd == "" {
				subcmd = arg
			}
		}
	}

	if cmd == "buildx" && subcmd == "build" {
		return runWithDepot(args)
	} else {
		return runWithDocker(args)
	}
}

func runWithDepot(args []string) error {
	binary, err := exec.LookPath("depot")
	if err != nil {
		binary, err = os.Executable()
		if err != nil {
			return errors.Wrap(err, "could not find depot binary")
		}
	}
	env := os.Environ()

	filteredArgs := []string{}
	done := false
	for _, arg := range args {
		if !done && arg == "buildx" {
			filteredArgs = append(filteredArgs, "depot")
			done = true
		} else {
			filteredArgs = append(filteredArgs, arg)
		}
	}

	return syscall.Exec(binary, append([]string{"depot"}, filteredArgs...), env)
}

func runWithDocker(args []string) error {
	original := path.Join(config.Dir(), "cli-plugins", "original-docker-buildx")
	if _, err := os.Stat(original); err != nil {
		return errors.Wrap(err, "could not find original docker-buildx plugin")
	}

	env := os.Environ()
	return syscall.Exec(original, append([]string{"docker-buildx"}, args...), env)
}
