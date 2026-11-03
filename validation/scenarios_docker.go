package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// dockerScenarios run Docker after `depot configure-docker`, which is how
// most users of Docker Compose and of plain `docker build` use Depot.
func dockerScenarios() []scenario {
	return []scenario{
		{
			Name:        "docker-plugin-build",
			Description: "docker build runs depot build through the Docker CLI plugin",
			Fixture:     "basic",
			Program:     "docker",
			Args:        []string{"build", ".", "--progress=plain", "--platform", "linux/amd64", "--target", "artifact", "-o", "type=local,dest=out"},
			Prepare:     configureDocker,
			Cleanup:     removeDriverContainers,
			Files:       []string{"out"},
			Expect:      expectation{FinishBuild: []string{"success"}},
		},
		{
			Name:        "docker-buildx-driver-build",
			Description: "docker buildx build runs through the Depot driver container and its buildkit proxy",
			Fixture:     "basic",
			Program:     "docker",
			Args:        []string{"buildx", "build", "--builder", "depot_{{project}}", "--progress=plain", "--platform", "linux/arm64", "--target", "artifact", "-o", "type=local,dest=out", "."},
			Prepare:     prepareBuildxDriver,
			Exclusive:   true,
			Cleanup:     removeDriverContainers,
			Files:       []string{"out"},
			Expect:      expectation{FinishBuild: []string{"success"}},
		},
		{
			Name:        "docker-buildx-driver-multiplatform",
			Description: "docker buildx build splits two platforms across both Depot driver containers",
			Fixture:     "basic",
			Program:     "docker",
			Args:        []string{"buildx", "build", "--builder", "depot_{{project}}", "--progress=plain", "--platform", "linux/amd64,linux/arm64", "--target", "artifact", "-o", "type=local,dest=out", "."},
			Prepare:     prepareBuildxDriver,
			Exclusive:   true,
			Cleanup:     removeDriverContainers,
			Files:       []string{"out"},
			Expect:      expectation{FinishBuild: []string{"success", "success"}},
		},
	}
}

// savedBuildScenarios save a build in the registry and then run a command
// that reads the saved build.
func savedBuildScenarios() []scenario {
	saveBuild := func(ctx context.Context, r *runInfo) error {
		_, err := r.command(ctx, "depot", "build", ".", "--progress=plain", "--platform", "linux/amd64,linux/arm64", "--save")
		return err
	}
	pushed := "{{registry-auth}}/validation/{{run}}:pushed"
	return []scenario{
		withImages(scenario{
			Name:        "pull-saved-build",
			Description: "depot pull loads a saved build into Docker",
			Fixture:     "basic",
			Args:        []string{"pull", "{{run}}-b1", "--platform", "linux/amd64", "-t", "validation-{{run}}:pulled", "--progress=plain"},
			Prepare:     saveBuild,
		}, "validation-{{run}}:pulled"),
		// depot push expects a registry that issues bearer tokens. The test
		// registry uses basic authentication, so the manifest upload fails
		// after every blob is copied.
		withRegistry(scenario{
			Name:        "push-saved-build",
			Description: "depot push copies the blobs of a saved multi-platform build to another registry",
			Fixture:     "basic",
			Args:        []string{"push", "{{run}}-b1", "--tag", pushed, "--progress=plain"},
			Prepare:     saveBuild,
			Expect:      expectation{ExitCode: 1, StderrContains: []string{"Pushing blob", "unexpected status code: 401"}},
		}, pushed),
	}
}

func configureDocker(ctx context.Context, r *runInfo) error {
	_, err := r.command(ctx, "depot", "configure-docker")
	return err
}

// prepareBuildxDriver configures Docker and then points the Depot driver
// containers at the validation API. buildx creates the containers again
// from the stored driver options.
func prepareBuildxDriver(ctx context.Context, r *runInfo) error {
	if err := configureDocker(ctx, r); err != nil {
		return err
	}
	apiURL := r.API.containerURL()

	instance := filepath.Join(r.dockerConfigDir(), "buildx", "instances", "depot_"+r.Project)
	data, err := os.ReadFile(instance)
	if err != nil {
		return err
	}
	var group map[string]any
	if err := json.Unmarshal(data, &group); err != nil {
		return err
	}
	nodes, _ := group["Nodes"].([]any)
	for _, n := range nodes {
		node, _ := n.(map[string]any)
		opts, _ := node["DriverOpts"].(map[string]any)
		if opts == nil {
			return fmt.Errorf("builder node in %s has no driver options", instance)
		}
		opts["env.DEPOT_API_URL"] = apiURL
		opts["env.DEPOT_ERROR_TELEMETRY"] = "0"
		opts["env.DEPOT_DISABLE_OTEL"] = "1"
	}
	data, err = json.Marshal(group)
	if err != nil {
		return err
	}
	if err := os.WriteFile(instance, data, 0o600); err != nil {
		return err
	}

	// Create the driver containers as `depot configure-docker` does, with
	// the address of the validation API added.
	removeDriverContainers(ctx, r)
	for _, arch := range []string{"amd64", "arm64"} {
		name := "buildx_buildkit_depot_" + r.Project + "_" + arch
		if _, err := dockerCommand(ctx, "create", "--name", name, "--privileged", "--init",
			"--add-host", "host.docker.internal:host-gateway",
			"--mount", "type=volume,source="+name+"_state,target=/var/lib/buildkit",
			"-e", "DEPOT_PROJECT_ID="+r.Project, "-e", "DEPOT_TOKEN="+r.Token, "-e", "DEPOT_PLATFORM="+arch,
			"-e", "DEPOT_API_URL="+apiURL, "-e", "DEPOT_ERROR_TELEMETRY=0", "-e", "DEPOT_DISABLE_OTEL=1",
			"public.ecr.aws/depot/cli:"+r.Binary.Version, "buildkitd"); err != nil {
			return err
		}
	}
	return nil
}

func removeDriverContainers(ctx context.Context, r *runInfo) {
	for _, arch := range []string{"amd64", "arm64"} {
		name := "buildx_buildkit_depot_" + r.Project + "_" + arch
		_, _ = dockerCommand(ctx, "rm", "-f", "-v", name)
		_, _ = dockerCommand(ctx, "volume", "rm", "-f", name+"_state")
	}
}
