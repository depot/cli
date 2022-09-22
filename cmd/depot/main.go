package main

import (
	"fmt"
	"log"
	"os"

	"github.com/depot/cli/internal/build"
	"github.com/depot/cli/internal/update"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/cmd/root"
	"github.com/depot/cli/pkg/config"
	"github.com/getsentry/sentry-go"
	"github.com/mattn/go-isatty"
	"github.com/mgutz/ansi"
)

func main() {
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

	rootCmd := root.NewCmdRoot(buildVersion, buildDate)

	if err := rootCmd.Execute(); err != nil {
		return 1
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

func checkForUpdate(currentVersion string) (*api.ReleaseResponse, error) {
	if !shouldCheckForUpdate() {
		return nil, nil
	}

	client, err := api.NewDepotFromEnv(config.GetApiToken())
	if err != nil {
		return nil, err
	}

	stateFilePath, err := config.StateFile()
	if err != nil {
		return nil, err
	}

	return update.CheckForUpdate(client, stateFilePath, currentVersion)
}

func shouldCheckForUpdate() bool {
	if os.Getenv("DEPOT_NO_UPDATE_NOTIFIER") != "" {
		return false
	}
	return !isCI() && isTerminal(os.Stdout) && isTerminal(os.Stderr)
}

func isCI() bool {
	return os.Getenv("CI") != "" || // GitHub Actions, Travis CI, CircleCI, Cirrus CI, GitLab CI, AppVeyor, CodeShip, dsari
		os.Getenv("BUILD_NUMBER") != "" || // Jenkins, TeamCity
		os.Getenv("RUN_ID") != "" // TaskCluster, dsari
}

func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
