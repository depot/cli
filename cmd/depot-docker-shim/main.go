package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func main() {
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

	if cmd == "build" {
		runWithDepot(args)
	} else if cmd == "buildx" && subcmd == "build" {
		filteredArgs := []string{}
		filtered := false
		for _, arg := range args {
			if !filtered && arg == "buildx" {
				filtered = true
				continue
			}
			filteredArgs = append(filteredArgs, arg)
		}
		runWithDepot(filteredArgs)
	} else if cmd == "depot" {
		filteredArgs := []string{}
		filtered := false
		for _, arg := range args {
			if !filtered && arg == "depot" {
				filtered = true
				continue
			}
			filteredArgs = append(filteredArgs, arg)
		}
		runWithDepot(filteredArgs)
	} else {
		runWithDocker(args)
	}
}

func runWithDepot(args []string) {
	binary, err := exec.LookPath("depot")
	if err != nil {
		panic(err)
	}
	env := os.Environ()
	err = syscall.Exec(binary, append([]string{"depot"}, args...), env)
	if err != nil {
		panic(err)
	}
}

func runWithDocker(args []string) {
	self, err := os.Executable()
	if err != nil {
		panic(err)
	}

	out, err := exec.Command("which", "-a", "docker").Output()
	if err != nil {
		panic(err)
	}
	candidates := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, candidate := range candidates {
		if candidate == self {
			continue
		}

		env := os.Environ()
		err = syscall.Exec(candidate, append([]string{"docker"}, args...), env)
		if err != nil {
			panic(err)
		}
	}
}
