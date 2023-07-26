package main

import (
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"syscall"

	"github.com/depot/cli/internal/build"
	"github.com/depot/cli/internal/update"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/ci"
	"github.com/depot/cli/pkg/cmd/root"
	"github.com/depot/cli/pkg/config"
	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli-plugins/manager"
	"github.com/docker/cli/cli-plugins/plugin"
	"github.com/docker/cli/cli/command"
	dockerConfig "github.com/docker/cli/cli/config"
	"github.com/getsentry/sentry-go"
	"github.com/mattn/go-isatty"
	"github.com/mgutz/ansi"
	"github.com/pkg/errors"
)

func main() {
	binary := os.Args[0]
	if strings.HasSuffix(binary, "-buildx") {
		cmd, subcmd := parseCmdSubcmd()
		if cmd == "buildx" && (subcmd == "build" || subcmd == "bake") {
			os.Args = append([]string{binary}, rewriteBuildxArgs()...)
		} else {
			err := runOriginalBuildx(os.Args[1:])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	}

	code := runMain()
	os.Exit(code)
}

func runMain() int {
	if os.Getenv("DEPOT_ERROR_TELEMETRY") != "0" {
		err := sentry.Init(sentry.ClientOptions{
			Dsn:         "https://e88a8bb8644346b99e02de76f47d936a@o1152282.ingest.sentry.io/6271909",
			Environment: build.SentryEnvironment,
			Release:     build.Version,
		})
		if err != nil {
			log.Fatalf("sentry.Init: %s", err)
		}
	}

	buildVersion := build.Version
	buildDate := build.Date

	updateMessageChan := make(chan *api.ReleaseResponse)
	go func() {
		rel, _ := checkForUpdate(buildVersion)
		updateMessageChan <- rel
	}()

	if plugin.RunningStandalone() {
		rootCmd := root.NewCmdRoot(buildVersion, buildDate)

		if err := rootCmd.Execute(); err != nil {
			return 1
		}
	} else {
		cmd, err := command.NewDockerCli()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}

		rootCmd := root.NewCmdRoot(buildVersion, buildDate)

		err = plugin.RunPlugin(cmd, rootCmd, manager.Metadata{
			SchemaVersion: "0.1.0",
			Vendor:        "Depot Technologies Inc.",
			Version:       buildVersion,
			URL:           "https://depot.dev",
		})

		if err != nil {
			if sterr, ok := err.(cli.StatusError); ok {
				if sterr.Status != "" {
					fmt.Fprintln(cmd.Err(), sterr.Status)
				}
				// StatusError should only be used for errors, and all errors should
				// have a non-zero exit status, so never exit with 0
				if sterr.StatusCode == 0 {
					return 1
				}
				return sterr.StatusCode
			}
			fmt.Fprintln(cmd.Err(), err)
			return 1
		}
	}

	newRelease := <-updateMessageChan
	if newRelease != nil {
		isHomebrew := update.IsUnderHomebrew()
		fmt.Fprintf(os.Stderr, "\n\n%s%s%s %s → %s\n",
			ansi.Color("A new release of depot is available, released on ", "yellow"),
			ansi.Color(newRelease.PublishedAt.Format("2006-01-02"), "yellow"),
			ansi.Color(":", "yellow"),
			ansi.Color(buildVersion, "cyan"),
			ansi.Color(newRelease.Version, "cyan"))
		if isHomebrew {
			fmt.Fprintf(os.Stderr, "To upgrade, run: %s\n", "brew update && brew upgrade depot/tap/depot")
		}
		fmt.Fprintf(os.Stderr, "%s\n\n",
			ansi.Color(fmt.Sprintf("https://github.com/depot/cli/releases/tag/v%s", newRelease.Version), "yellow"))
	}

	return 0
}

func parseCmdSubcmd() (string, string) {
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

	return cmd, subcmd
}

func rewriteBuildxArgs() []string {
	args := os.Args[1:]
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
	return filteredArgs
}

func runOriginalBuildx(args []string) error {
	original := path.Join(dockerConfig.Dir(), "cli-plugins", "original-docker-buildx")
	if _, err := os.Stat(original); err != nil {
		return errors.Wrap(err, "could not find original docker-buildx plugin")
	}

	env := os.Environ()
	return syscall.Exec(original, append([]string{"docker-buildx"}, args...), env)
}

func checkForUpdate(currentVersion string) (*api.ReleaseResponse, error) {
	if !shouldCheckForUpdate() {
		return nil, nil
	}

	stateFilePath, err := config.StateFile()
	if err != nil {
		return nil, err
	}

	return update.CheckForUpdate(stateFilePath, currentVersion)
}

func shouldCheckForUpdate() bool {
	if os.Getenv("DEPOT_NO_UPDATE_NOTIFIER") != "" {
		return false
	}
	_, isCI := ci.Provider()
	return !isCI && isTerminal(os.Stdout) && isTerminal(os.Stderr)
}

func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
