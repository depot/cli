package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/depot/cli/pkg/builder"
	"github.com/depot/cli/pkg/buildx/build"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/progress"
	printer "github.com/docker/buildx/util/progress"
	"golang.org/x/vuln/client"
)

func main() {
	token := os.Getenv("DEPOT_TOKEN")
	project := os.Getenv("DEPOT_PROJECT_ID")

	// You can use a context with timeout to cancel the build if you would like.
	ctx := context.Background()

	// 1. Register a new build.
	req := helpers.NewSDKRequest(project, build.Options{}, helpers.UsingDepotFeatures{})
	build, err := helpers.BeginBuild(ctx, req, token)
	if err != nil {
		log.Fatal(err)
	}

	// Set the buildErr to any error that represents the build failing.
	var buildErr error
	defer build.Finish(buildErr)

	// 2. Start progress reporter. This will report the build progress logs to the
	// Depot API and print it to the terminal.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reporter, buildErr := progress.NewProgress(ctx, build.ID, token, printer.PrinterModePlain)
	if buildErr != nil {
		return
	}
	reporter.Run(ctx) // canceling the ctx reports remaining logs to Depot.

	// 3. Start buildkit machine.
	finishLogSpan := reporter.StartLog("[depot] launching amd64 builder")
	builder := builder.NewBuilder(token, build.ID, "amd64")
	buildkit, buildErr := builder.StartBuildkit(ctx)
	finishLogSpan(buildErr)
	if buildErr != nil {
		return
	}

	// 4. Check buildkitd readiness. When the buildkitd starts, it can take
	// quite a while to be ready to accept connections when it loads a large
	// cache boltdb.
	finishLogSpan = reporter.StartLog("[depot] connecting to amd64 builder")
	retries := 120            // try 120 times
	retryAfter := time.Second // wait one second between retries
	buildkitClient, buildErr := buildkit.WaitUntilReady(ctx, retries, retryAfter)
	finishLogSpan(buildErr)
	if buildErr != nil {
		return
	}

	buildErr = buildImage(ctx, buildkitClient, reporter, build.ID)
}

func buildImage(ctx context.Context, buildkitClient *client.Client, reporter *progress.Progress, buildID string) error {
	return nil
}
