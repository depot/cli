package main

import (
	"context"
	"encoding/base64"
	"log"
	"os"
	"time"

	"github.com/depot/cli/pkg/build"
	"github.com/depot/cli/pkg/machine"
	"github.com/depot/cli/pkg/progress"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	printer "github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
)

func main() {
	token := os.Getenv("DEPOT_TOKEN")
	project := os.Getenv("DEPOT_PROJECT_ID")

	// You can use a context with timeout to cancel the build if you would like.
	ctx := context.Background()

	// 1. Register a new build.
	req := &cliv1.CreateBuildRequest{
		ProjectId: project,
		Options: []*cliv1.BuildOptions{
			{
				Command: cliv1.Command_COMMAND_BUILD,
				Tags:    []string{"depot/example:latest"},
			},
		},
	}
	build, err := build.NewBuild(ctx, req, token)
	if err != nil {
		log.Fatal(err)
	}

	// Set the buildErr to any error that represents the build failing.
	var buildErr error
	defer build.Finish(buildErr)

	// 2. Start progress reporter. This will report the build progress logs to the
	// Depot API and print it to the terminal.
	ctx, cancel := context.WithCancel(ctx)
	reporter, finishReporter, buildErr := progress.NewProgress(ctx, build.ID, build.Token, progress.Plain)
	if buildErr != nil {
		return
	}

	defer finishReporter() // Required to ensure that the printer is stopped before context `cancel()`.
	defer cancel()

	// 3. Acquire a buildkit machine.
	var buildkit *machine.Buildkit
	buildErr = reporter.WithLog("[depot] launching amd64 machine", func() error {
		buildkit, buildErr = machine.Acquire(ctx, build.ID, build.Token, "amd64")
		return buildErr
	})
	if buildErr != nil {
		return
	}
	defer buildkit.Release()

	// 4. Check buildkitd readiness. When the buildkitd starts, it may take
	// quite a while to be ready to accept connections when it loads a large boltdb.
	var buildkitClient *client.Client
	buildErr = reporter.WithLog("[depot] connecting to amd64 machine", func() error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		buildkitClient, buildErr = buildkit.Connect(ctx)
		return buildErr
	})
	if buildErr != nil {
		return
	}

	// 5. Use the buildkit client to build the image.
	buildErr = buildImage(ctx, buildkitClient, reporter)
	if buildErr != nil {
		return
	}
}

func buildImage(ctx context.Context, buildkitClient *client.Client, reporter *progress.Progress) error {
	statusCh, done := printer.NewChannel(reporter)
	defer func() { <-done }()

	ops := llb.Image("alpine:latest")
	def, err := ops.Marshal(ctx, llb.LinuxAmd64)
	if err != nil {
		return err
	}

	opts := client.SolveOpt{
		FrontendAttrs: map[string]string{
			"platform": "linux/amd64",
		},
		Internal: true, // Prevent recording the build steps and traces in buildkit as it is _very_ slow.
	}
	res, err := buildkitClient.Solve(ctx, def, opts, statusCh)
	if err != nil {
		return err
	}

	for k, encoded := range res.ExporterResponse {
		v, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return err
		}
		log.Printf("exporter response: %v %v\n", k, string(v))
	}
	return nil
}
