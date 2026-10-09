# Validation harness

The validation harness runs the Depot CLI as a user would and checks the results. It runs each scenario twice: once with a baseline CLI built from a git revision, and once with the CLI in your working tree. It then compares what each run produced.

Use it to check that a change keeps the behavior of the CLI the same. It replaces unit tests for the build, bake, pull, push, and Docker plugin commands.

## What a run does

1. Builds the CLI from your working tree. This is the candidate.
2. Builds the CLI from the baseline revision (`main` by default) in a git worktree.
3. Builds a container image for each CLI. `--load` uses this image as its registry proxy, and `docker buildx` uses it as the Depot driver.
4. Starts these containers:
   - Two buildkitd daemons with mutual TLS. They stand in for the amd64 and arm64 Depot builders.
   - A TLS container registry.
   - A second TLS registry that requires a password, for `depot push`.
   - A git server. It serves each fixture directory as a repository.
5. Starts a local server that implements the Depot build and push APIs.
6. Runs every scenario with both CLIs. Each run executes in a new container that shares the network of the builders, and has its own home directory and Docker configuration.
7. Records these observations for each run:
   - the exit code
   - standard output
   - the lines of standard error that are not progress output
   - the files the scenario names
   - the images loaded into Docker
   - the manifests pushed to the registry
   - the calls the CLI made to the API
8. Reports each scenario with one of these statuses:

| Status | Meaning |
| --- | --- |
| `PASS` | Both runs met the expectations of the scenario, and their observations are the same. |
| `PASS (accepted differences)` | The runs differ only in ways that the scenario lists as accepted. |
| `DIFFER` | The runs differ in a way that the scenario does not accept. |
| `FAIL` | A run did not meet the expectations of the scenario. |

The harness exits with status 1 if any scenario has the status `DIFFER` or `FAIL`.

Before it compares runs, the harness replaces values that change from run to run with fixed placeholders. These values are:

- build and run identifiers
- digests, timestamps, durations, and sizes
- temporary paths
- container addresses

It also sorts values that earlier versions of the CLI wrote in random order.

## Requirements

- Docker. The harness mounts its work directory (`-work`, a temporary directory by default) into containers, so Docker must be able to share that directory. The harness has been tested with OrbStack on macOS.
- Git and Go.
- A buildkitd image. By default this is `depot-validation/buildkitd:private`, which you build from the Depot buildkit repository:

  ```sh
  docker buildx build --target buildkit-linux -t depot-validation/buildkitd:private --load ../buildkit-private
  ```

  To use upstream buildkit instead, pass `-upstream`. The harness then uses `moby/buildkit:v0.13.2` and skips the scenarios that need the Depot buildkit exporter: fast `--load` through the registry proxy, and `--sbom-dir`. CI runs the harness this way, because it cannot build the Depot image.

## Run the harness

Run every scenario against `main`:

```sh
go run ./validation
```

Common options:

| Option | Effect |
| --- | --- |
| `-upstream` | Uses upstream buildkit and skips the scenarios that need the Depot buildkit exporter. |
| `-run 'bake-.*'` | Runs only the scenarios whose names match the regular expression. |
| `-baseline v2.80.0` | Compares with another git revision. |
| `-baseline none` | Checks the expectations of the candidate without a comparison. |
| `-parallel 2` | Sets how many scenarios run at the same time. |
| `-count 3` | Runs each scenario three times, to find results that change from run to run. The order of the two CLIs alternates between repetitions. |
| `-latency 25ms` | Delays every packet between the CLI and the builders, to measure the effect of round trips. |
| `-env KEY=VALUE` | Sets an environment variable for every run. |
| `-verbose` | Prints each difference in full. |
| `-report report.json` | Writes every observation to a JSON file. Each observation includes the duration of the run. |
| `-keep` | Keeps the directory of each run, including a file with its environment. |
| `-hold` | Keeps the containers and the API running after the scenarios, until you press Ctrl-C. |
| `-list` | Lists the scenarios. |

## Add a scenario

Scenarios are defined in the `scenarios*.go` files. Fixtures are in `fixtures/`, and each run copies its fixture into a new directory.

Arguments can contain these placeholders:

| Placeholder | Value |
| --- | --- |
| `{{run}}` | A name that is unique to the run. Use it in image tags. |
| `{{registry}}` | The address of the test registry. |
| `{{registry-auth}}` | The address of the registry that requires a password. |
| `{{project}}` | The project of the run. |
| `{{git}}` | The URL of the git server. Append `/<fixture>.git` to name a repository. |
| `{{workdir}}` | The directory of the run. |

A scenario can also:

- run `docker` instead of `depot`, with `Program`
- run steps before and after the program, with `Prepare` and `Cleanup`
- run alone after the other scenarios, with `Exclusive`
- change the API responses, with `API`
- send an interrupt signal after a delay, with `Interrupt`

If a scenario shows a difference that is intended, add the field and the reason to `Accept`. The field names are listed in the report, for example `stderr`, `api`, or `file:out`. If the baseline CLI has a defect that the candidate fixes, describe it in `BaselineDefect`.
