package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func allScenarios() []scenario {
	var all []scenario
	all = append(all, commandLineScenarios()...)
	all = append(all, buildScenarios()...)
	all = append(all, bakeScenarios()...)
	all = append(all, dockerScenarios()...)
	all = append(all, savedBuildScenarios()...)
	all = append(all, extendedScenarios()...)
	return all
}

func build(name, description, fixture string, args ...string) scenario {
	return scenario{
		Name:        name,
		Description: description,
		Fixture:     fixture,
		Args:        append([]string{"build", ".", "--progress=plain"}, args...),
		Expect:      expectation{FinishBuild: []string{"success"}},
	}
}

func bake(name, description, fixture string, args ...string) scenario {
	return scenario{
		Name:        name,
		Description: description,
		Fixture:     fixture,
		Args:        append([]string{"bake", "--progress=plain"}, args...),
		Expect:      expectation{FinishBuild: []string{"success"}},
	}
}

func offline(name, description, fixture string, args ...string) scenario {
	return scenario{Name: name, Description: description, Fixture: fixture, Args: args}
}

func withStdin(s scenario, stdin string) scenario {
	s.Stdin = stdin
	return s
}

func failing(s scenario, exitCode int, stderr ...string) scenario {
	s.Expect.ExitCode = exitCode
	s.Expect.StderrContains = stderr
	if s.Expect.FinishBuild != nil {
		s.Expect.FinishBuild = nil
	}
	return s
}

func commandLineScenarios() []scenario {
	return []scenario{
		offline("help-build", "flags and usage text of depot build", "", "build", "--help"),
		offline("help-bake", "flags and usage text of depot bake", "", "bake", "--help"),
		failing(offline("build-without-context", "build without a context argument fails", "basic", "build"), 1, "requires exactly 1 argument"),
		failing(offline("build-unknown-flag", "build with an unknown flag fails", "basic", "build", ".", "--no-such-flag"), 1, "unknown flag"),
		failing(withEnv(offline("build-missing-token", "build without a token fails before contacting the API", "basic", "build", "."), "DEPOT_TOKEN", "<unset>"), 1, "missing API token"),
		failing(offline("build-quiet-progress-conflict", "--quiet with --progress=plain is rejected", "basic", "build", ".", "-q", "--progress=plain"), 1, "quiet cannot be used together"),
		failing(offline("build-no-cache-filter-conflict", "--no-cache with --no-cache-filter is rejected", "basic", "build", ".", "--no-cache", "--no-cache-filter", "build"), 1, "cannot currently be used together"),
		failing(offline("build-invalid-platform", "an invalid --platform value is rejected", "basic", "build", ".", "--platform", "linux/not-an-arch/x/y"), 1),
		failing(offline("build-invalid-secret", "an invalid --secret value is rejected", "basic", "build", ".", "--secret", "id=,src=x"), 1),
		failing(offline("build-push-local-conflict", "--push with a local output is rejected", "basic", "build", ".", "--push", "-o", "type=local,dest=out"), 1, "can't be used together"),
		failing(offline("build-invalid-build-platform", "an invalid --build-platform value is rejected", "basic", "build", ".", "--build-platform", "windows/amd64"), 1, "invalid build platform"),
		offline("bake-print-default", "bake --print renders the default group", "bake", "bake", "--print"),
		offline("bake-print-all", "bake --print renders a group with matrix targets", "bake", "bake", "--print", "all"),
		offline("bake-print-overrides", "bake --print applies --set overrides", "bake", "bake", "--print",
			"--set", "*.platform=linux/arm64", "--set", "app.tags=registry.invalid/override:1", "--set", "app.args.MESSAGE=override", "--set", "artifact.no-cache=true"),
		withEnv(offline("bake-print-variable-env", "bake --print reads variables from the environment", "bake", "bake", "--print", "app"), "TAG", "from-env", "REGISTRY", "registry.example"),
		offline("bake-print-file", "bake --print with an explicit -f file", "bake", "bake", "-f", "docker-bake.hcl", "--print", "artifact"),
		accept(offline("bake-print-compose", "bake --print converts a compose file", "compose", "bake", "--print"),
			"stdout", "buildx v0.38 omits the empty network field"),
		accept(offline("bake-print-compose-project", "bake --print resolves x-depot.project-id through inheritance and file order", "composeproject",
			"bake", "--print", "api", "fromcompose", "worker"),
			"stdout", "buildx v0.38 omits the empty network field"),
		expectStdout(withEnv(offline("bake-print-source-date-epoch", "bake --print shows SOURCE_DATE_EPOCH from the environment", "epoch",
			"bake", "--print", "default", "pinned"), "SOURCE_DATE_EPOCH", "1700000000"), `"SOURCE_DATE_EPOCH": "1700000000"`, `"SOURCE_DATE_EPOCH": "1600000000"`),
		offline("bake-print-project-reference", "bake --print with an argument that reads the project_id of another target", "projectref",
			"bake", "-f", "docker-bake.hcl", "--print", "artifact"),
		expectStdout(offline("bake-print-project-reference-json", "bake --print with a JSON argument that reads the project_id of another target", "projectref",
			"bake", "-f", "docker-bake.json", "--print", "json"), `"LITERAL": "target._json.project_id"`, `"NESTED": "<project>-json"`),
		expectStdout(offline("bake-print-description-reference", "bake --print with an argument that reads the description of another target", "description",
			"bake", "--print", "artifact"), `"MESSAGE": "described by _base"`),
		offline("bake-print-multiproject", "bake --print shows per-target project identifiers", "multiproject", "bake", "--print"),
		accept(offline("bake-print-linked", "bake --print with target contexts", "linked", "bake", "--print", "child"),
			"stdout", "buildx v0.38 shows the cacheonly output that it gives to linked targets"),
		failing(offline("bake-print-invalid", "bake --print reports HCL syntax errors", "invalid", "bake", "--print"), 1),
		offline("bake-print-json-project", "bake --print with project_id in a JSON definition", "jsonproject", "bake", "--print"),
		offline("bake-print-shared-context", "bake --print with targets that share a context", "sharedcontext", "bake", "--print"),
		failing(offline("bake-print-remote", "bake --print rejects a remote definition", "basic", "bake", "--print", "https://github.com/depot/cli.git"), 1, "cannot use remote target with --print"),
		failing(offline("bake-print-unknown-target", "bake --print rejects an unknown target", "bake", "bake", "--print", "missing"), 1),
	}
}

func baselineDefect(s scenario, reason string) scenario {
	s.BaselineDefect = reason
	return s
}

func accept(s scenario, field, reason string) scenario {
	if s.Accept == nil {
		s.Accept = map[string]string{}
	}
	s.Accept[field] = reason
	return s
}

func withEnv(s scenario, kv ...string) scenario {
	if s.Env == nil {
		s.Env = map[string]string{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		s.Env[kv[i]] = kv[i+1]
	}
	return s
}

func withFiles(s scenario, files ...string) scenario {
	s.Files = append(s.Files, files...)
	return s
}

func withImages(s scenario, refs ...string) scenario {
	s.Images = append(s.Images, refs...)
	return s
}

func withRegistry(s scenario, refs ...string) scenario {
	s.Registry = append(s.Registry, refs...)
	return s
}

func withAPI(s scenario, b apiBehavior) scenario {
	s.API = b
	return s
}

func expectStdout(s scenario, substrings ...string) scenario {
	s.Expect.StdoutContains = append(s.Expect.StdoutContains, substrings...)
	return s
}

func expectStderr(s scenario, substrings ...string) scenario {
	s.Expect.StderrContains = append(s.Expect.StderrContains, substrings...)
	return s
}

func expectFinish(s scenario, results ...string) scenario {
	s.Expect.FinishBuild = results
	return s
}

func expectCheck(s scenario, check func(o *observation) error) scenario {
	s.Expect.Check = check
	return s
}

func requireFileContains(path, want string) func(o *observation) error {
	return func(o *observation) error {
		if !strings.Contains(o.Files[path], want) {
			return fmt.Errorf("file %s does not contain %q:\n%s", path, want, o.Files[path])
		}
		return nil
	}
}

// lintDockerfile has a MAINTAINER instruction, which the linters report.
const lintDockerfile = "FROM busybox:1.36 as build\nMAINTAINER validation@example.com\nRUN echo lint > /lint.txt\nFROM scratch\nCOPY --from=build /lint.txt /\n"

// writeBzip2Context writes a bzip2 tar archive that contains only the given
// Dockerfile to context.tar.bz2 in the run directory.
func writeBzip2Context(dockerfile string) func(ctx context.Context, r *runInfo) error {
	return func(ctx context.Context, r *runInfo) error {
		dir := filepath.Join(r.WorkDir, "archive")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
			return err
		}
		_, err := r.command(ctx, "sh", "-c", "tar -C "+dir+" -c . | bzip2 > "+filepath.Join(r.WorkDir, "context.tar.bz2"))
		return err
	}
}

// requireFileContent checks the content of a file inside a recorded
// directory.
func requireFileContent(dir, path, content string) func(o *observation) error {
	return func(o *observation) error {
		want := path + " " + shortHash([]byte(content))
		if !slices.Contains(strings.Split(o.Files[dir], "\n"), want) {
			return fmt.Errorf("%s/%s does not contain %q:\n%s", dir, path, content, o.Files[dir])
		}
		return nil
	}
}

func allOf(checks ...func(o *observation) error) func(o *observation) error {
	return func(o *observation) error {
		for _, check := range checks {
			if err := check(o); err != nil {
				return err
			}
		}
		return nil
	}
}

// requireImagesPresent checks that a number of the recorded images are in
// Docker.
func requireImagesPresent(n int) func(o *observation) error {
	return func(o *observation) error {
		present := 0
		for _, image := range o.Images {
			if image.Present {
				present++
			}
		}
		if present != n {
			return fmt.Errorf("%d images are in Docker, want %d", present, n)
		}
		return nil
	}
}

// requireStderrCount checks how many lines of standard error contain a text.
func requireStderrCount(text string, n int) func(o *observation) error {
	return func(o *observation) error {
		count := 0
		for _, line := range strings.Split(o.Stderr, "\n") {
			if strings.Contains(line, text) {
				count++
			}
		}
		if count != n {
			return fmt.Errorf("%d lines of standard error contain %q, want %d", count, text, n)
		}
		return nil
	}
}

// loadsImage records the image validation-{{run}}:latest and requires it
// to be in Docker after the run.
func loadsImage(s scenario) scenario {
	return expectCheck(withImages(s, "validation-{{run}}:latest"), func(o *observation) error {
		for _, image := range o.Images {
			if image.Present {
				return nil
			}
		}
		return errors.New("the image is not in Docker")
	})
}

// requireConnectedPlatforms checks the builder platforms that the CLI
// connected to.
func requireConnectedPlatforms(want ...string) func(o *observation) error {
	return func(o *observation) error {
		if !slices.Equal(o.API.ConnectedPlatforms, want) {
			return fmt.Errorf("connected platforms are %v, want %v", o.API.ConnectedPlatforms, want)
		}
		return nil
	}
}

// requireTargetProjects checks the project of the build that each target
// was created in.
func requireTargetProjects(want map[string]string) func(o *observation) error {
	return func(o *observation) error {
		got := map[string]string{}
		for _, raw := range o.API.CreateBuild {
			var req struct {
				ProjectID string `json:"projectId"`
				Options   []struct {
					TargetName string `json:"targetName"`
				} `json:"options"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return err
			}
			for _, opt := range req.Options {
				got[opt.TargetName] = req.ProjectID
			}
		}
		if !maps.Equal(got, want) {
			return fmt.Errorf("target projects are %v, want %v", got, want)
		}
		return nil
	}
}

func buildScenarios() []scenario {
	push := "{{registry}}/validation/{{run}}:latest"
	return []scenario{
		withFiles(build("build-local-output", "single-platform build exported to a local directory", "basic",
			"--platform", "linux/amd64", "--target", "artifact", "--build-arg", "MESSAGE=hello", "-o", "type=local,dest=out"), "out"),
		withFiles(build("build-local-multiplatform", "two-platform build split across both builders", "basic",
			"--platform", "linux/amd64,linux/arm64", "--target", "artifact", "-o", "type=local,dest=out"), "out"),
		withFiles(build("build-tar-output", "build exported as a tar file", "basic",
			"--platform", "linux/arm64", "--target", "artifact", "-o", "type=tar,dest=out.tar"), "out.tar"),
		withFiles(build("build-dockerignore", "files listed in .dockerignore are not sent", "basic",
			"--target", "artifact", "--platform", "linux/arm64", "-o", "type=local,dest=out"), "out"),
		withRegistry(build("build-push-single", "single-platform image pushed to a registry", "basic",
			"--platform", "linux/amd64", "-t", push, "--push"), push),
		withRegistry(build("build-push-multiplatform", "two-platform image pushed and merged into one index", "basic",
			"--platform", "linux/amd64,linux/arm64", "-t", push, "--push"), push),
		withRegistry(build("build-push-annotations", "index and manifest annotations on a multi-platform push", "basic",
			"--platform", "linux/amd64,linux/arm64", "-t", push, "--push",
			"--annotation", "index:org.opencontainers.image.description=validation-index",
			"--annotation", "manifest:org.opencontainers.image.title=validation-manifest"), push),
		withRegistry(build("build-push-provenance-sbom", "push with SBOM and maximum provenance attestations", "basic",
			"--platform", "linux/amd64", "-t", push, "--push", "--sbom=true", "--provenance=mode=max"), push),
		withRegistry(build("build-push-no-provenance", "push with provenance disabled", "basic",
			"--platform", "linux/amd64", "-t", push, "--push", "--provenance=false"), push),
		withImages(build("build-load", "single-platform image loaded into the local Docker daemon", "basic",
			"--platform", "linux/amd64", "--load", "-t", "validation-{{run}}:latest"), "validation-{{run}}:latest"),
		withImages(build("build-load-native", "native-platform image loaded into Docker", "basic",
			"--load", "-t", "validation-{{run}}:latest"), "validation-{{run}}:latest"),
		withImages(build("build-load-multiplatform", "two-platform build with --load loads the image for the host platform", "basic",
			"--platform", "linux/amd64,linux/arm64", "--load", "-t", "validation-{{run}}:latest"), "validation-{{run}}:latest"),
		withImages(withAPI(build("build-load-registry", "--load through the Depot registry when the API requests it", "basic",
			"--platform", "linux/amd64", "--load", "-t", "validation-{{run}}:latest"), apiBehavior{LoadUsingRegistry: true}), "validation-{{run}}:latest"),
		baselineDefect(loadsImage(withFiles(build("build-load-local-output", "--load with a local output loads the image and writes the output", "basic",
			"--platform", "linux/amd64", "--load", "-t", "validation-{{run}}:latest", "-o", "type=local,dest=out"), "out")),
			"the old CLI ignored --load when another output was set"),
		baselineDefect(loadsImage(withFiles(withAPI(build("build-load-registry-local-output", "--load through the Depot registry with a local output", "basic",
			"--platform", "linux/amd64", "--load", "-t", "validation-{{run}}:latest", "-o", "type=local,dest=out"), apiBehavior{LoadUsingRegistry: true}), "out")),
			"the old CLI rejected --load through the Depot registry together with another output"),
		expectStderr(loadsImage(withAPI(build("build-load-retry", "--load builds again with a docker export when the pull fails", "basic",
			"--platform", "linux/amd64", "--load", "-t", "validation-{{run}}:latest"), apiBehavior{LoadUsingRegistry: true, PullFails: true})), "fast load failed; retrying"),
		baselineDefect(expectStderr(loadsImage(withFiles(withAPI(build("build-load-retry-local-output", "--load with a local output builds again with both outputs when the pull fails", "basic",
			"--platform", "linux/amd64", "--load", "-t", "validation-{{run}}:latest", "-o", "type=local,dest=out"), apiBehavior{LoadUsingRegistry: true, PullFails: true}), "out")), "fast load failed; retrying"),
			"the old CLI rejected --load through the Depot registry together with another output"),
		baselineDefect(loadsImage(withFiles(withAPI(build("build-load-registry-docker-output", "--load through the Depot registry with a docker tar output", "basic",
			"--platform", "linux/amd64", "--load", "-t", "validation-{{run}}:latest", "-o", "type=docker,dest=out.tar"), apiBehavior{LoadUsingRegistry: true}), "out.tar")),
			"the old CLI rejected --load through the Depot registry together with another output"),
		withImages(build("build-load-untagged", "--load without a tag", "basic", "--platform", "linux/arm64", "--load")),
		expectStderr(withRegistry(build("build-save", "--save pushes the image to the Depot registry", "basic",
			"--platform", "linux/amd64", "--save"), "{{registry}}/{{project}}:{{run}}-b1"), "depot pull"),
		withRegistry(build("build-save-tags", "--save with --save-tag adds custom tags", "basic",
			"--platform", "linux/amd64", "--save", "--save-tag", "{{run}}-custom"), "{{registry}}/{{project}}:{{run}}-b1", "{{registry}}/{{project}}:{{run}}-custom"),
		accept(withFiles(build("build-metadata-push", "--metadata-file after a push", "basic",
			"--platform", "linux/amd64,linux/arm64", "-t", push, "--push", "--metadata-file", "metadata.json"), "metadata.json"),
			"file:metadata.json", "the old CLI combined the responses of the two builders in random order; the new CLI uses the order of --platform"),
		withFiles(build("build-metadata-local", "--metadata-file after a local export", "basic",
			"--platform", "linux/amd64", "--target", "artifact", "-o", "type=local,dest=out", "--metadata-file", "metadata.json"), "metadata.json"),
		withFiles(withImages(build("build-iidfile", "--iidfile writes the image identifier", "basic",
			"--platform", "linux/amd64", "--load", "-t", "validation-{{run}}:latest", "--iidfile", "iid.txt"), "validation-{{run}}:latest"), "iid.txt"),
		expectCheck(withFiles(withRegistry(build("build-iidfile-push-multiplatform", "--iidfile after a multi-platform push writes the index digest", "basic",
			"--platform", "linux/amd64,linux/arm64", "-t", push, "--push", "--iidfile", "iid.txt"), push), "iid.txt"),
			requireFileContains("iid.txt", "<digest>")),
		expectStdout(withImages(scenario{
			Name: "build-quiet", Description: "--quiet prints only the image identifier", Fixture: "basic",
			Args:   []string{"build", ".", "-q", "--platform", "linux/amd64", "--load", "-t", "validation-{{run}}:latest"},
			Expect: expectation{FinishBuild: []string{"success"}},
		}, "validation-{{run}}:latest"), "<digest>"),
		expectCheck(withEnv(withFiles(build("build-secrets", "secrets from a file and from an environment variable", "secrets",
			"--platform", "linux/amd64", "--secret", "id=token,src=token.txt", "--secret", "id=envsecret,env=VT_SECRET", "-o", "type=local,dest=out"), "out"),
			"VT_SECRET", "env-secret-value"), requireFileContains("out", "env.txt")),
		expectCheck(withEnv(withFiles(build("build-source-date-epoch", "SOURCE_DATE_EPOCH from the environment becomes a build argument", "epoch",
			"--platform", "linux/amd64", "-o", "type=local,dest=out"), "out"), "SOURCE_DATE_EPOCH", "1700000000"),
			requireFileContent("out", "s", "sde=1700000000\n")),
		expectCheck(withEnv(withFiles(build("build-source-date-epoch-build-arg", "--build-arg SOURCE_DATE_EPOCH takes precedence over the environment", "epoch",
			"--platform", "linux/amd64", "--build-arg", "SOURCE_DATE_EPOCH=1600000000", "-o", "type=local,dest=out"), "out"), "SOURCE_DATE_EPOCH", "1700000000"),
			requireFileContent("out", "s", "sde=1600000000\n")),
		withFiles(build("build-named-context", "--build-context adds a named context", "contexts",
			"--platform", "linux/amd64", "--build-context", "extra=extra", "-o", "type=local,dest=out"), "out"),
		withFiles(scenario{
			Name: "build-dockerfile-stdin", Description: "Dockerfile read from standard input with -f -", Fixture: "basic",
			Args:   []string{"build", ".", "--progress=plain", "-f", "-", "--platform", "linux/amd64", "-o", "type=local,dest=out"},
			Stdin:  "FROM busybox:1.36 AS build\nRUN mkdir /out && echo stdin > /out/stdin.txt\nFROM scratch\nCOPY --from=build /out /\n",
			Expect: expectation{FinishBuild: []string{"success"}},
		}, "out"),
		withFiles(scenario{
			Name: "build-context-stdin", Description: "Dockerfile as the whole context from standard input", Fixture: "basic",
			Args:   []string{"build", "-", "--progress=plain", "--platform", "linux/amd64", "-o", "type=local,dest=out"},
			Stdin:  "FROM busybox:1.36 AS build\nRUN mkdir /out && echo stdin-context > /out/stdin.txt\nFROM scratch\nCOPY --from=build /out /\n",
			Expect: expectation{FinishBuild: []string{"success"}},
		}, "out"),
		withFiles(build("build-system-options", "--add-host, --ulimit, and --shm-size reach the build", "sysopts",
			"--platform", "linux/amd64", "--add-host", "validation.invalid:10.1.2.3", "--ulimit", "nofile=2048:2048", "--shm-size", "128m", "-o", "type=local,dest=out"), "out"),
		withFiles(build("build-no-cache-pull", "--no-cache and --pull", "basic",
			"--platform", "linux/amd64", "--target", "artifact", "--no-cache", "--pull", "-o", "type=local,dest=out"), "out"),
		withRegistry(build("build-cache-registry", "--cache-to and --cache-from with a registry cache", "basic",
			"--platform", "linux/amd64", "--target", "artifact", "-o", "type=local,dest=out",
			"--cache-to", "type=registry,ref={{registry}}/validation/{{run}}-cache:latest,mode=max",
			"--cache-from", "type=registry,ref={{registry}}/validation/{{run}}-cache:latest"), "{{registry}}/validation/{{run}}-cache:latest"),
		withFiles(build("build-build-platform", "--build-platform forces every platform onto one builder", "basic",
			"--platform", "linux/amd64,linux/arm64", "--build-platform", "linux/arm64", "--target", "artifact", "-o", "type=local,dest=out"), "out"),
		withFiles(withAPI(build("build-pending-gzip", "builder connection is pending first and uses gzip", "basic",
			"--platform", "linux/amd64", "--target", "artifact", "-o", "type=local,dest=out"), apiBehavior{PendingConnections: 3, Gzip: true}), "out"),
		build("build-no-output", "build with no output keeps the result in the build cache", "basic", "--platform", "linux/amd64"),
		withEnv(build("build-no-output-suppressed", "DEPOT_SUPPRESS_NO_OUTPUT_WARNING hides the warning", "basic", "--platform", "linux/amd64"),
			"DEPOT_SUPPRESS_NO_OUTPUT_WARNING", "1"),
		accept(expectFinish(failing(build("build-failure", "a failing RUN step returns an error", "failing", "--platform", "linux/amd64"), 1, "exit code: 3"), "error: <any>"),
			"stderr", "buildx v0.38 shows the log lines of the failed step in the error summary"),
		withFiles(scenario{
			Name: "build-remote-git", Description: "build from a git repository context", Fixture: "basic",
			Args:   []string{"build", "{{git}}/remote.git", "--progress=plain", "--platform", "linux/amd64", "--target", "artifact", "-o", "type=local,dest=out"},
			Expect: expectation{FinishBuild: []string{"success"}},
		}, "out"),
		withFiles(scenario{
			Name: "build-remote-git-lint", Description: "--lint with a git repository context", Fixture: "secrets",
			Args:   []string{"build", "{{git}}/remote.git", "--progress=plain", "--lint", "--platform", "linux/amd64", "--target", "artifact", "-o", "type=local,dest=out"},
			Expect: expectation{FinishBuild: []string{"success"}},
		}, "out"),
		withFiles(withRegistry(build("build-sbom-dir", "--sbom-dir saves SBOM attestations", "basic",
			"--platform", "linux/amd64,linux/arm64", "-t", push, "--push", "--sbom=true", "--sbom-dir", "sboms"), push), "sboms"),
		failing(build("build-missing-dockerfile", "a missing Dockerfile returns an error", "basic", "-f", "Missing.Dockerfile"), 1),
		failing(withAPI(build("build-create-denied", "the API rejects the build", "basic"), apiBehavior{CreateBuildCode: 7}), 1),
		withFiles(build("build-lint-pass", "--lint with a clean Dockerfile", "basic", "--lint", "--platform", "linux/amd64", "--target", "artifact", "-o", "type=local,dest=out"), "out"),
		withFiles(build("build-lint-warn", "--lint reports problems without failing", "lint", "--lint", "--lint-fail-on", "none", "--platform", "linux/amd64", "-o", "type=local,dest=out"), "out"),
		failing(build("build-lint-fail", "--lint-fail-on=warn fails the build", "lint", "--lint", "--lint-fail-on", "warn", "--platform", "linux/amd64", "-o", "type=local,dest=out"), 1),
		failing(build("build-lint-remote-dockerfile", "--lint downloads a Dockerfile given as a URL", "lint",
			"--lint", "--lint-fail-on", "warn", "-f", "{{git}}/files/lint/Dockerfile", "--platform", "linux/amd64", "-o", "type=local,dest=out"), 1, "DL4000"),
		withStdin(failing(scenario{
			Name: "build-lint-remote-context-stdin", Description: "--lint with a git repository context and a Dockerfile from standard input", Fixture: "basic",
			Args: []string{"build", "{{git}}/basic.git", "--progress=plain", "--lint", "--lint-fail-on", "warn", "-f", "-", "--platform", "linux/amd64", "-o", "type=local,dest=out"},
		}, 1, "DL4000"), lintDockerfile),
		baselineDefect(failing(scenario{
			Name: "build-lint-stdin-bzip2-context", Description: "--lint reads the Dockerfile from a bzip2 context archive on standard input", Fixture: "basic",
			Args:      []string{"build", "-", "--progress=plain", "--lint", "--lint-fail-on", "warn", "--platform", "linux/amd64", "-o", "type=local,dest=out"},
			StdinFile: "context.tar.bz2",
			Prepare:   writeBzip2Context(lintDockerfile),
		}, 1, "DL4000"), "the old CLI linted ./Dockerfile instead of the Dockerfile in the archive"),
		withEnv(withFiles(build("build-print-outline", "--print outline in experimental mode", "basic", "--print", "outline"), "out"), "BUILDX_EXPERIMENTAL", "1"),
		expectFinish(failing(scenario{
			Name: "build-interrupt", Description: "an interrupt during a build cancels it", Fixture: "slow",
			Args:      []string{"build", ".", "--progress=plain", "--platform", "linux/amd64"},
			Interrupt: 8 * time.Second,
		}, 1), "canceled"),
		accept(expectFinish(failing(withAPI(scenario{
			Name: "build-api-cancel", Description: "the API cancels the build through the health check", Fixture: "slow",
			Args: []string{"build", ".", "--progress=plain", "--platform", "linux/amd64", "--no-cache"},
		}, apiBehavior{CancelAfter: 8 * time.Second}), 1), "canceled"),
			"stderr", "the CLI's message for a canceled build now matches the error text of buildkit v0.34"),
	}
}

func bakeScenarios() []scenario {
	return []scenario{
		withFiles(bake("bake-local", "bake one target to a local directory", "bake", "artifact", "--set", "*.platform=linux/amd64"), "out"),
		withFiles(bake("bake-default-group", "bake the default group", "bake", "--set", "*.platform=linux/arm64"), "out"),
		withFiles(bake("bake-matrix", "bake a group that contains matrix targets", "bake", "all", "--set", "*.platform=linux/amd64"), "out"),
		withRegistry(bake("bake-push", "bake pushes a multi-platform target", "bake", "app", "--push",
			"--set", "app.tags={{registry}}/validation/{{run}}:latest", "--set", "app.platform=linux/amd64,linux/arm64"), "{{registry}}/validation/{{run}}:latest"),
		withImages(bake("bake-load", "bake loads a target into Docker", "bake", "app", "--load",
			"--set", "app.tags=validation-{{run}}:latest", "--set", "app.platform=linux/amd64"), "validation-{{run}}:latest"),
		withImages(withAPI(bake("bake-load-registry", "bake --load through the Depot registry", "bake", "app", "--load",
			"--set", "app.tags=validation-{{run}}:latest", "--set", "app.platform=linux/amd64"), apiBehavior{LoadUsingRegistry: true}), "validation-{{run}}:latest"),
		baselineDefect(loadsImage(withFiles(withAPI(bake("bake-load-registry-local-output", "bake --load through the Depot registry with a target that has a local output", "bake", "app", "--load",
			"--set", "app.tags=validation-{{run}}:latest", "--set", "app.platform=linux/amd64", "--set", "app.output=type=local,dest=out"), apiBehavior{LoadUsingRegistry: true}), "out")),
			"the old CLI rejected --load through the Depot registry together with another output"),
		baselineDefect(expectStderr(loadsImage(withFiles(withAPI(bake("bake-load-retry-local-output", "bake --load with a local output builds again with both outputs when the pull fails", "bake", "app", "--load",
			"--set", "app.tags=validation-{{run}}:latest", "--set", "app.platform=linux/amd64", "--set", "app.output=type=local,dest=out"), apiBehavior{LoadUsingRegistry: true, PullFails: true}), "out")), "fast load failed; retrying"),
			"the old CLI rejected --load through the Depot registry together with another output"),
		baselineDefect(loadsImage(withFiles(bake("bake-load-local-output", "bake --load with a target that has a local output", "bake", "app", "--load",
			"--set", "app.tags=validation-{{run}}:latest", "--set", "app.platform=linux/amd64", "--set", "app.output=type=local,dest=out"), "out")),
			"the old CLI ignored --load when another output was set"),
		expectCheck(withEnv(withFiles(bake("bake-source-date-epoch", "bake passes SOURCE_DATE_EPOCH from the environment unless the target sets it", "epoch",
			"default", "pinned", "--set", "*.platform=linux/amd64"), "out"), "SOURCE_DATE_EPOCH", "1700000000"),
			allOf(requireFileContent("out", "default/s", "sde=1700000000\n"), requireFileContent("out", "pinned/s", "sde=1600000000\n"))),
		baselineDefect(expectStdout(bake("bake-call-outline", "a target with call = outline prints the outline", "call", "outline", "--set", "*.platform=linux/amd64"),
			"TARGET: artifact"), "the old CLI ignored the call attribute and ran a normal build"),
		baselineDefect(expectStdout(bake("bake-call-targets", "a target with call = targets prints the targets", "call", "targets", "--set", "*.platform=linux/amd64"),
			"artifact (default)"), "the old CLI ignored the call attribute and ran a normal build"),
		baselineDefect(expectStdout(failing(bake("bake-call-check", "a target with call = check prints the warnings and fails", "call", "check", "--set", "*.platform=linux/amd64"), 1),
			"Check complete, 1 warning has been found!", "FromAsCasing"), "the old CLI ignored the call attribute and ran a normal build"),
		baselineDefect(expectStdout(failing(bake("bake-call-check-builtin", "call = check without a syntax directive", "call", "check-builtin", "--set", "*.platform=linux/amd64"), 1),
			"FromAsCasing"), "the old CLI ignored the call attribute and ran a normal build"),
		baselineDefect(expectStdout(failing(bake("bake-call-check-json", "call = check,format=json prints the warnings as JSON", "call", "check-json", "--set", "*.platform=linux/amd64"), 1),
			`"warnings"`, "FromAsCasing"), "the old CLI ignored the call attribute and ran a normal build"),
		baselineDefect(expectStdout(bake("bake-call-check-ignorestatus", "call = check,ignorestatus=true prints the warnings and succeeds", "call", "check-ignorestatus", "--set", "*.platform=linux/amd64"),
			"FromAsCasing"), "the old CLI ignored the call attribute and ran a normal build"),
		baselineDefect(failing(bake("bake-policy", "a target with a source policy fails, because Depot builders cannot enforce it", "policy", "denied", "--set", "*.platform=linux/amd64"), 1,
			`target "denied" sets a source policy, but Depot builders do not support source policies`), "the old CLI ignored the policy attribute and built every source"),
		withFiles(bake("bake-call-build", "a target with call = build builds normally", "call", "build", "--set", "*.platform=linux/amd64"), "out"),
		baselineDefect(expectCheck(withFiles(withImages(withAPI(withEnv(bake("bake-load-pattern", "bake --load with a target pattern loads every matching target", "pattern",
			"mx-*", "--load", "--metadata-file", "metadata.json", "--set", "*.platform=linux/amd64"), "RUN", "{{run}}"), apiBehavior{LoadUsingRegistry: true}),
			"validation-{{run}}-a:latest", "validation-{{run}}-b:latest"), "metadata.json"),
			allOf(requireFileContains("metadata.json", `"mx-a"`), requireFileContains("metadata.json", `"mx-b"`), requireImagesPresent(2))),
			"the old CLI did not accept target patterns"),
		expectCheck(withFiles(bake("bake-project-reference", "a build argument reads the project_id of another target", "projectref",
			"-f", "docker-bake.hcl", "artifact", "--set", "*.platform=linux/amd64"), "out"),
			requireFileContent("out", "message.txt", "vtproject-ref\n")),
		expectCheck(withFiles(bake("bake-description-reference", "a build argument reads the description of another target", "description",
			"artifact", "--set", "*.platform=linux/amd64"), "out"),
			requireFileContent("out", "message.txt", "described by _base\n")),
		withRegistry(bake("bake-save", "bake --save pushes each target to the Depot registry", "bake", "app", "--save",
			"--set", "app.platform=linux/amd64"), "{{registry}}/{{project}}:{{run}}-b1-app"),
		accept(withFiles(bake("bake-metadata", "bake --metadata-file", "bake", "artifact", "app", "--metadata-file", "metadata.json",
			"--set", "*.platform=linux/amd64", "--set", "app.tags={{registry}}/validation/{{run}}:latest", "--set", "app.output=type=registry"), "metadata.json"),
			"api", "buildx v0.38 adds unpack=false to registry outputs"),
		expectFinish(withFiles(bake("bake-multiproject", "targets with different project_id values become separate builds", "multiproject",
			"--set", "*.platform=linux/amd64"), "out"), "success", "success"),
		accept(withFiles(bake("bake-linked", "a target that uses another target as a context", "linked", "child", "--set", "*.platform=linux/amd64"), "out"),
			"api", "buildx v0.38 gives linked targets a cacheonly output"),
		expectCheck(expectFinish(withFiles(bake("bake-compose-project", "x-depot.project-id applies to inheriting targets, and HCL project_id overrides it", "composeproject",
			"api", "fromcompose", "worker", "--set", "*.platform=linux/amd64"), "out"), "success", "success"),
			requireTargetProjects(map[string]string{"api": "vtproject-compose", "fromcompose": "vtproject-compose", "worker": "vtproject-hcl"})),
		withImages(expectFinish(bake("bake-compose", "bake builds compose services across projects", "compose", "--load",
			"--set", "web.tags=validation-{{run}}-web:latest", "--set", "worker.tags=validation-{{run}}-worker:latest", "--set", "worker.platform=linux/amd64"),
			"success", "success"), "validation-{{run}}-web:latest", "validation-{{run}}-worker:latest"),
		accept(expectFinish(failing(bake("bake-failure", "a failing target fails the bake", "failing"), 1, "exit code: 3"), "error: <any>"),
			"stderr", "buildx v0.38 shows the failed step and its log lines in the error summary"),
		failing(bake("bake-unknown-target", "an unknown target fails", "bake", "missing"), 1),
		withFiles(bake("bake-shared-context", "targets that share a context use their own ignore files", "sharedcontext",
			"--set", "*.platform=linux/amd64"), "out"),
		expectFinish(withFiles(bake("bake-json-project", "project_id in a JSON definition selects a second project", "jsonproject",
			"--set", "*.platform=linux/amd64"), "out"), "success", "success"),
		baselineDefect(withFiles(bake("bake-remote", "bake a definition from a git repository", "basic", "{{git}}/remote.git", "artifact",
			"--set", "artifact.platform=linux/amd64"), "out"), "the CLI dereferenced nil project options for a remote definition"),
		baselineDefect(failing(bake("bake-remote-multiproject", "a remote definition with targets of another project is rejected", "basic", "{{git}}/multiproject.git",
			"--set", "*.platform=linux/amd64"), 1, "a remote bake definition can build targets of only one project"), "the CLI dereferenced nil project options for a remote definition"),
		withFiles(withEnv(bake("bake-variable-env", "bake reads variables from the environment", "bake", "artifact",
			"--set", "artifact.platform=linux/amd64", "--set", "artifact.args.MESSAGE=${TAG}"), "TAG", "from-env"), "out"),
	}
}
