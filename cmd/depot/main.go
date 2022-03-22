package main

import (
	"log"
	"os"

	"github.com/depot/cli/internal/build"
	"github.com/depot/cli/pkg/cmd/root"
	"github.com/getsentry/sentry-go"
)

func main() {
	code := runMain()
	os.Exit(code)
}

func runMain() int {
	if os.Getenv("DEPOT_ERROR_TELEMETRY") != "0" {
		err := sentry.Init(sentry.ClientOptions{
			Dsn: "https://e88a8bb8644346b99e02de76f47d936a@o1152282.ingest.sentry.io/6271909",
		})
		if err != nil {
			log.Fatalf("sentry.Init: %s", err)
		}
	}

	buildVersion := build.Version
	buildDate := build.Date

	rootCmd := root.NewCmdRoot(buildVersion, buildDate)

	if err := rootCmd.Execute(); err != nil {
		return 1
	}

	return 0
}
