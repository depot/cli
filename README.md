# Depot CLI [![CI](https://github.com/depot/cli/actions/workflows/ci.yml/badge.svg)](https://github.com/depot/cli/actions/workflows/ci.yml)

Official CLI for [Depot](https://depot.dev) - you can use the CLI to build Docker images via Depot's remote builder infrastructure.

---

> ⚡ **Depot** provides cloud-hosted container builder machines - our builders are quick, running on native hardware. Build caching is fully managed with no extra configuration.
>
> [More information →](https://depot.dev)

---

- [Depot CLI ](#depot-cli-)
  - [Installation](#installation)
  - [Quick Start](#quick-start)
  - [Usage](#usage)
    - [`depot bake`](#depot-bake)
      - [Flags for `bake`](#flags-for-bake)
    - [`depot build`](#depot-build)
      - [Flags for `build`](#flags-for-build)
      - [`depot build cancel`](#depot-build-cancel)
    - [`depot cache`](#depot-cache)
      - [`depot cache reset`](#depot-cache-reset)
    - [`depot checks`](#depot-checks)
    - [`depot exec`](#depot-exec)
    - [`depot gocache`](#depot-gocache)
    - [`depot configure-docker`](#depot-configure-docker)
    - [`depot jobs`](#depot-jobs)
      - [`depot jobs metrics`](#depot-jobs-metrics)
      - [`depot jobs analytics`](#depot-jobs-analytics)
    - [`depot list`](#depot-list)
      - [`depot list projects`](#depot-list-projects)
      - [`depot list builds`](#depot-list-builds)
      - [`depot list jobs`](#depot-list-jobs)
    - [`depot projects`](#depot-projects)
      - [`depot projects create`](#depot-projects-create)
      - [`depot projects delete`](#depot-projects-delete)
      - [`depot projects list`](#depot-projects-list)
    - [`depot init`](#depot-init)
    - [`depot login`](#depot-login)
    - [`depot logout`](#depot-logout)
    - [`depot org`](#depot-org)
      - [`depot org list`](#depot-org-list)
      - [`depot org show`](#depot-org-show)
      - [`depot org switch`](#depot-org-switch)
    - [`depot pull`](#depot-pull)
    - [`depot push`](#depot-push)
    - [`depot pull-token`](#depot-pull-token)
    - [`depot version`](#depot-version)
  - [Contributing](#contributing)
  - [License](#license)

## Installation

For Mac, you can install the CLI with Homebrew:

```
brew install depot/tap/depot
```

For Linux, you can install with our installation script:

```sh
# Install the latest version
curl -L https://depot.dev/install-cli.sh | sh

# Install a specific version
curl -L https://depot.dev/install-cli.sh | sh -s 2.17.0
```

To install with the [Proto](https://moonrepo.dev/docs/proto) toolchain manager, run:

```sh
proto plugin add depot "https://raw.githubusercontent.com/depot/cli/refs/heads/main/proto.yaml
proto install depot
```

For all other platforms, you can download the binary directly from [the latest release](https://github.com/depot/cli/releases).

## Quick Start

1. Run `depot login` to authenticate with your Depot account.
2. `cd` to your project directory.
3. Run `depot init` to link the local directory with a Depot project - this will create a `depot.json` file in the current directory.
4. Run `depot build -t repo/image:tag .`

## Usage

Set `DEPOT_IN_AUTOMATION=1` to suppress build summary URLs, saved-image pull/push hints, and new-version notifications in scripts and CI. Any non-empty value enables this behavior; build progress and errors are still printed.

### `depot bake`

Run a Docker build from a HCL, JSON, or Compose file using Depot's remote builder infrastructure. This command accepts all the command line flags as Docker's `docker buildx bake` command, you can run `depot bake --help` for the full list.

The `bake` command needs to know which [project](https://depot.dev/docs/core-concepts#projects) id to route the build to. For passing the project id you have four options available to you:

1. Run `depot init` at the root of your repository and commit the resulting `depot.json` file
2. Use the `--project` flag in your `depot bake` command
3. Set the `DEPOT_PROJECT_ID` environment variable which will be automatically detected.
4. Use [`x-depot` ](http://depot.dev/docs/cli/reference#compose-support) extension field in your `docker-compose.yml` file.

By default, `depot bake` will leave the built image in the remote builder cache. If you would like to download the image to your local Docker daemon (for instance, to `docker run` the result), you can use the `--load` flag.

Alternatively, to push the image to a remote registry directly from the builder instance, you can use the `--push` flag.

The `bake` command allows you to define all of your build targets in a central file, either HCL, JSON, or Compose. You can then pass that file to the `bake` command and Depot will build all of the target images with all of their options (i.e. platforms, tags, build arguments, etc.).

**Example**

An example `docker-bake.hcl` file:

```hcl
group "default" {
  targets = ["original", "db"]
}

target "original" {
  dockerfile = "Dockerfile"
  platforms = ["linux/amd64", "linux/arm64"]
  tags = ["example/app:test"]
}

target "db" {
  dockerfile = "Dockerfile.db"
  platforms = ["linux/amd64", "linux/arm64"]
  tags = ["example/db:test"]
}
```

To build all of the images we just need to call `bake`:

```shell
depot bake -f docker-bake.hcl
```

If you want to build a specific target in the bake file, you can specify it in the `bake` command:

```shell
depot bake -f docker-bake.hcl original
```

#### compose support

Depot supports using bake to build [Docker Compose](https://depot.dev/blog/depot-with-docker-compose) files.

To use `depot bake` with a Docker Compose file, you can specify the file with the `-f` flag:

```shell
depot bake -f docker-compose.yml
```

Compose files have special extensions prefixed with `x-` to give additional information to the build process.

In this example, the `x-bake` extension is used to specify the tags for each service
and the `x-depot` extension is used to specify different project IDs for each.

```yaml
services:
  mydb:
    build:
      dockerfile: ./Dockerfile.db
      x-bake:
        tags:
          - ghcr.io/myorg/mydb:latest
          - ghcr.io/myorg/mydb:v1.0.0
      x-depot:
        project-id: 1234567890
  myapp:
    build:
      dockerfile: ./Dockerfile.app
      x-bake:
        tags:
          - ghcr.io/myorg/myapp:latest
          - ghcr.io/myorg/myapp:v1.0.0
      x-depot:
        project-id: 9876543210
```

#### Flags for `bake`

| Name             | Description                                                                                               |
| ---------------- | --------------------------------------------------------------------------------------------------------- |
| `build-platform` | Run builds on this platform ("dynamic", "linux/amd64", "linux/arm64") (default "dynamic")                 |
| `file`           | Build definition file                                                                                     |
| `help`           | Show the help doc for `bake`                                                                              |
| `lint`           | Lint Dockerfiles of targets before the build                                                              |
| `lint-fail-on`   | Set the lint severity that fails the build ("info", "warn", "error", "none") (default "error")            |
| `load`           | Shorthand for "--set=\*.output=type=docker"                                                               |
| `metadata-file`  | Write build result metadata to the file                                                                   |
| `no-cache`       | Do not use cache when building the image                                                                  |
| `print`          | Print the options without building                                                                        |
| `progress`       | Set type of progress output ("auto", "plain", "tty"). Use plain to show container output (default "auto") |
| `project`        | Depot project ID                                                                                          |
| `provenance`     | Shorthand for "--set=\*.attest=type=provenance"                                                           |
| `pull`           | Always attempt to pull all referenced images                                                              |
| `push`           | Shorthand for "--set=\*.output=type=registry"                                                             |
| `save`           | Saves bake targets to the Depot registry                                                                  |
| `sbom`           | Shorthand for "--set=\*.attest=type=sbom"                                                                 |
| `set`            | Override target value (e.g., "targetpattern.key=value")                                                   |
| `token`          | Depot API token                                                                                           |

### `depot build`

Runs a Docker build using Depot's remote builder infrastructure. This command accepts all the command line flags as Docker's `docker buildx build` command, you can run `depot build --help` for the full list.

The `build` command needs to know which [project](https://depot.dev/docs/core-concepts#projects) id to route the build to. For passing the project id you have three options available to you:

1. Run `depot init` at the root of your repository and commit the resulting `depot.json` file
2. Use the `--project` flag in your `depot build` command
3. Set the `DEPOT_PROJECT_ID` environment variable which will be automatically detected

By default, `depot build` will leave the built image in the remote builder cache. If you would like to download the image to your local Docker daemon (for instance, to `docker run` the result), you can use the `--load` flag.

Alternatively, to push the image to a remote registry directly from the builder instance, you can use the `--push` flag.

**Example**

```shell
# Build remotely
depot build -t repo/image:tag .
```

```shell
# Build remotely, download the container locally
depot build -t repo/image:tag . --load
```

```shell
# Build remotely, push to a registry
depot build -t repo/image:tag . --push
```

#### Flags for `build`

| Name              | Description                                                                                               |
| ----------------- | --------------------------------------------------------------------------------------------------------- |
| `add-host`        | Add a custom host-to-IP mapping (format: "host:ip")                                                       |
| `allow`           | Allow extra privileged entitlement (e.g., "network.host", "security.insecure")                            |
| `attest`          | Attestation parameters (format: "type=sbom,generator=image")                                              |
| `build-arg`       | Set build-time variables                                                                                  |
| `build-context`   | Additional build contexts (e.g., name=path)                                                               |
| `build-platform`  | Run builds on this platform ("dynamic", "linux/amd64", "linux/arm64") (default "dynamic")                 |
| `cache-from`      | External cache sources (e.g., "user/app:cache", "type=local,src=path/to/dir")                             |
| `cache-to`        | Cache export destinations (e.g., "user/app:cache", "type=local,dest=path/to/dir")                         |
| `cgroup-parent`   | Optional parent cgroup for the container                                                                  |
| `file`            | Name of the Dockerfile (default: "PATH/Dockerfile")                                                       |
| `help`            | Show help doc for `build`                                                                                 |
| `iidfile`         | Write the image ID to the file                                                                            |
| `label`           | Set metadata for an image                                                                                 |
| `lint`            | Lint Dockerfile before the build                                                                          |
| `lint-fail-on`    | Set the lint severity that fails the build ("info", "warn", "error", "none") (default "error")            |
| `load`            | Shorthand for "--output=type=docker"                                                                      |
| `metadata-file`   | Write build result metadata to the file                                                                   |
| `network`         | Set the networking mode for the "RUN" instructions during build (default "default")                       |
| `no-cache`        | Do not use cache when building the image                                                                  |
| `no-cache-filter` | Do not cache specified stages                                                                             |
| `output`          | Output destination (format: "type=local,dest=path")                                                       |
| `platform`        | Set target platform for build                                                                             |
| `progress`        | Set type of progress output ("auto", "plain", "tty"). Use plain to show container output (default "auto") |
| `project`         | Depot project ID                                                                                          |
| `provenance`      | Shorthand for "--attest=type=provenance"                                                                  |
| `pull`            | Always attempt to pull all referenced images                                                              |
| `push`            | Shorthand for "--output=type=registry"                                                                    |
| `quiet`           | Suppress the build output and print image ID on success                                                   |
| `save`            | Saves build to the Depot registry                                                                         |
| `sbom`            | Shorthand for "--attest=type=sbom"                                                                        |
| `secret`          | Secret to expose to the build (format: "id=mysecret[,src=/local/secret]")                                 |
| `shm-size`        | Size of "/dev/shm"                                                                                        |
| `ssh`             | SSH agent socket or keys to expose to the build                                                           |
| `tag`             | Name and optionally a tag (format: "name:tag")                                                            |
| `target`          | Set the target build stage to build                                                                       |
| `token`           | Depot API token                                                                                           |
| `ulimit`          | Ulimit options (default [])                                                                               |

#### `depot build cancel`

Request cancellation of container builds using an organization API token supplied
through `DEPOT_TOKEN` or `--token`.

```shell
depot build cancel <build-id> [<build-id>...]
depot build cancel --project <project-id> --all-running --dry-run
depot build cancel --project <project-id> --all-running --yes --output json
```

Cancellation is asynchronous. Finished builds are successful no-ops; failures are
reported per build and cause a nonzero exit status. Bulk cancellation selects all
running builds before requesting confirmation; use `--yes` for scripts.

See [build cancellation](docs/build-cancellation.md) for authentication, selection,
and output details. To build a directory named `cancel`, use `depot build ./cancel`.

### `depot cache`

Interact with the cache associated with a Depot project. The `cache` command consists of subcommands for each operation.

#### `depot cache reset`

Reset the cache of the Depot project to force a new empty cache volume to be created.

**Example**

Reset the cache of the current project ID in the root `depot.json`

```shell
depot cache reset .
```

Reset the cache of a specific project ID

```shell
depot cache reset --project 12345678910
```

### `depot checks`

Manage Agent Checks and saved review agents. Requires Agent Checks access in the selected organization.

```sh
depot checks list --repo github:depot/api --outcome rejected
depot checks get <check-id-or-attempt-id>
depot checks attempts <check-id-or-attempt-id> -n 100 --output json
depot checks run <agent-id> --repo github:depot/api --ref feature
depot checks retry <check-id-or-attempt-id>
depot checks transcript <check-id-or-attempt-id> --output json --output-file transcript.jsonl
depot checks resolve <check-id-or-attempt-id> --finding 0
depot checks unresolve <check-id-or-attempt-id> --all

depot checks agents list
depot checks agents get <agent-id>
depot checks agents create --name Security --instructions-file review.md --repo github:depot/api
depot checks agents update <agent-id> --model default
depot checks agents models
```

A check ID selects its latest attempt; an attempt ID selects that execution. Results include both IDs, revision, state,
outcome, summary, findings with original zero-based indexes, resolutions, and available execution details. JSON follows
CI's snake_case convention: `run`/`retry` return `check_id` and `attempt_id`; `get` returns the selected attempt directly.
Check lists return arrays of `{check_id, created_at, current_attempt}`; history returns an array of attempts.
Resolve/unresolve return `{check_id, created_at, current_attempt}`. Each attempt includes structured `result` when its
stored JSON is valid, plus the original `result_json` string when present. Agent reads/saves return the configuration
directly (`agent_id`, `name`, `instructions`, `model_id`, `repos`, `events`); agent lists return an array of configurations.
Models output includes `default_model_id` and `models`. Transcript `--output json` emits the exact JSONL archive;
`--output jsonl` is an equivalent alias.

List tables shorten long display names, repositories and branches, and show 12-character head SHAs. IDs remain complete;
`get` and JSON retain full values. Transcript file exports confirm the destination and saved byte count on stderr after
the file is saved successfully. API errors include the failed operation while preserving the underlying error status.

`list` supports `--agent`, `--repo`, `--branch` (requires `--repo`), `--sha` (head or base prefix), `--outcome`, and
`--query` (agent name, repository, branch, or SHA prefix). Outcomes are `running` (including queued), `done`, `passed`,
`findings` (a passing review with advice), `rejected`, `resolved`, and `failed` (execution failure).
`checks list`, `checks agents list`, and `checks attempts` use `-n` as a positive total result limit (default 50), matching CI run listing.
The CLI follows API pages internally until that limit is reached or there are no more matches; no `--all` or `--page-token` is exposed.

Repository selectors carry their forge: `github:owner/repo`, `cursor-origin:owner/repo`, or `code:name`.
`run` accepts `--ref` with an optional full `--commit-sha`, or `--head`, plus an optional `--base` for either mode.
By default it reviews the default branch's HEAD against its first parent (or the empty tree for an initial commit).
One-off runs do not change the agent's watched repositories. `retry` reuses the captured configuration and revisions,
creates a new attempt under the same check, and only accepts checks whose current execution failed. An explicitly
selected historical attempt cannot be retried or have its findings changed.

Agent create/update supports `--name`, `--instructions` or `--instructions-file` (`-` reads stdin), `--model`, repeatable
`--repo`, and repeatable `--event` (for example `github:opened`, `github:synchronize`, or `code:push`). Omitted update
fields are preserved; supplied repositories/events replace that selection. Use `--clear-repos` to remove all watches,
`--clear-events` to disable automatic checks, and `--model default` to follow the current default model. Creation defaults
to no watched repositories and the supported events except `github:push`.

Commands support `--org`, `--token` (or `DEPOT_TOKEN`), and `--output json`. Transcript output is readable text by default;
`--output jsonl` preserves the original archived bytes. Archives are verified against their size and SHA-256 before
output and are subject to availability and retention. There is no live transcript following or waiting option.
Run/retry return both IDs immediately. Exit 0 means the operation succeeded, including inspecting failed/rejected checks;
exit 1 means input, authentication/API, download, or output failure. Review outcomes are reported in the output.

### `depot exec`

Execute a command with an injected BuildKit connection. This command launches a remote machine in Depot's infrastructure, establishes a connection to it, and makes this connection available to the specified command through an environment variable.

**Example**

```shell
# Run a command with access to a remote BuildKit instance
depot exec --platform linux/amd64 docker build .

# Run a Dagger command using Depot's infrastructure
depot exec dagger run ...
```

#### Flags for `exec`

| Name       | Description                                                                      |
| ---------- | -------------------------------------------------------------------------------- |
| `env-var`  | Environment variable name for the BuildKit connection (default: "BUILDKIT_HOST") |
| `platform` | Platform to execute the command on (linux/amd64 or linux/arm64)                  |
| `project`  | Depot project ID                                                                 |
| `progress` | Set type of progress output ("auto", "plain", "tty")                             |
| `token`    | Depot token                                                                      |

### `depot gocache`

Configure Go tools to use Depot Cache. The Go tools will use the remote cache service to store and retrieve build artifacts.

_Note: This requires Go 1.25 or later._

Set the environment variable `GOCACHEPROG` to `depot gocache` to configure Go to use Depot Cache.

```shell
export GOCACHEPROG='depot gocache'
```

Next, run your Go build commands as usual.

```shell
go build ./...
```

To set verbose output, add the --verbose option:

```shell
export GOCACHEPROG='depot gocache --verbose'
```

To clean the cache, you can use the typical `go clean` workflow:

```shell
go clean -cache
```

If you are in multiple Depot organizations and want to specify the organization, you can use the `--organization` flag.

```shell
export GOCACHEPROG='depot gocache --organization ORG_ID'
```

### `depot configure-docker`

Configure Docker to use Depot's remote builder infrastructure. This command installs Depot as a Docker CLI plugin (i.e., `docker depot ...`) and sets the Depot plugin as the default Docker builder (i.e., `docker build`).

```shell
depot configure-docker
```

If you want to uninstall the plugin, you can specify the `--uninstall` flag.

```shell
depot configure-docker --uninstall
```

### `depot jobs`

List GitHub Actions jobs run on Depot, newest first, or inspect one job by its GitHub job ID.
This command is for Depot GitHub Actions runners; use `depot ci` for Depot CI executions.

```shell
depot jobs --repo acme/api --status finished --conclusion failure
depot jobs 123456789 --repo acme/api --output json
```

Listings include job and workflow names, status, conclusion, runner label, execution duration,
and queue time. Individual job details also include lifecycle timestamps, billable time,
requested CPU and memory, and the GitHub URL. Requested resources describe runner capacity,
not measured utilization. The API does not currently expose step timing breakdowns.

Use `--org` to select an organization (otherwise the current organization is used).
Authentication uses `--token`, `DEPOT_TOKEN`, or saved Depot credentials. Output defaults to a
readable table in a terminal and JSON when piped. Explicit `--output json` never prompts for
login. JSON follows the API's protobuf JSON format: field names use camelCase, enums use
symbolic names, and 64-bit integers are strings to preserve precision.

Filter listings with repeatable `--repo`, `--status`, `--conclusion`, and `--runner-label` flags.
Use `--query` to search job/workflow names or exact numeric job/run IDs.
A single-job lookup requires exactly one `--repo`; listing filters do not apply to it.

#### `depot jobs metrics`

Fetch CPU and memory utilization for one GitHub Actions job execution using its GitHub
job ID and exactly one `--repo owner/name`. Use `depot jobs --repo owner/name` to find job IDs.
Completed jobs return their execution metrics; running jobs return a snapshot that can grow
on subsequent requests.

```shell
depot jobs metrics 123456789 --repo acme/api
depot jobs metrics 123456789 --repo acme/api --org org_123 --output json
```

Uses the same authentication, organization selection, and `--output auto|table|json` behavior
as `depot jobs`. Tables show job/runner context, snapshot time, availability and reason,
observed timestamps, average/peak CPU and memory utilization, sample counts, and downsampling
details. JSON includes the full API response with `job`, `availability`, `stats`, `cap`,
`samples`, and `snapshotAt`. Samples are ordered by timestamp. CPU measures user-mode
utilization averaged across cores; memory measures used memory as a fraction of total memory.
Utilization is a fraction from 0 to 1 in JSON and a percentage in tables.

Unavailable measurements are omitted from JSON's optional utilization fields and displayed
as `n/a` in table summaries; measured zero remains zero. `availability` distinguishes available,
partial, and unavailable telemetry and includes the API's reason. Availability describes the
observed samples and does not guarantee coverage of the entire execution. Telemetry-store
errors fail the command instead of returning an empty success response.

The API limits execution windows to seven days and returns at most 5,000 samples. When samples
are downsampled, `cap` reports the raw/returned counts, limit, and strategy. The `evenly_spaced`
strategy retains the first and last samples but may omit intermediate peaks. Summary statistics
are calculated by the API across all observed per-second samples before downsampling. This
command has no time-range or pagination flags and does not expose per-step measurements.

#### `depot jobs analytics`

Show aggregate CPU and memory utilization (average and peak), average and p95 job duration,
and failure rate by repository, workflow, and job. Groups are ordered by descending job count.
These are metrics across job executions, not resource measurements for an individual job or step.
Analytics may lag completed jobs by up to 15 minutes; `dataThrough` identifies data freshness.

```shell
depot jobs analytics --repo acme/api --job test --output json
depot jobs analytics --start 2026-10-01T00:00:00Z --end 2026-10-08T00:00:00Z
```

Filter with repeatable `--repo`, `--workflow` (display name), `--workflow-path`, `--job`
(workflow job key), and `--runner-label` flags. Table utilization and failure rates are
percentages; JSON retains the API's fractions from 0 to 1. Durations are in seconds.

Both listings and analytics default to the last 30 days. For an explicit time window, supply
both `--start` (inclusive) and `--end` (exclusive) as RFC 3339 timestamps, at most 90 days apart.
They return one page, with `--page-size` from 1 to 250 (default 50). To continue, pass the
response's `nextPageToken` as `--page-token`, keeping all filters and the time range unchanged.
Tables print the continuation token when another page is available.

### `depot list`

Interact with Depot projects, builds, and GitHub Actions jobs.

#### `depot list projects`

Display an interactive listing of current Depot projects. Selecting a specific project will display the latest builds.
To return from the latest builds to projects, press `ESC`.

To exit type `q` or `ctrl+c`

**Example**

```shell
depot list projects
```

#### `depot list builds`

Display the latest Depot builds for a project. By default the command runs an interactive listing of depot builds showing status and build duration.

To exit type `q` or `ctrl+c`

**Example**

List builds for the project in the current directory.

```shell
depot list builds
```

**Example**

List builds for a specific project ID

```shell
depot list builds --project 12345678910
```

**Example**

The list command can output build information to stdout with the `--output` option. It supports `json` and `csv`.

Output builds in JSON for the project in the current directory.

```shell
depot list builds --output json
```

#### `depot list jobs`

List GitHub Actions jobs using the same flags and output as `depot jobs` without a job ID.

```shell
depot list jobs --repo acme/api --output json
```

### `depot projects`

Create and manage Depot projects.

#### `depot projects create`

Create a new Depot project. This command creates a new project in your Depot account with the specified configuration.

Use `--max-concurrent-builds` to set the number of concurrent builds per builder instance before horizontal autoscaling adds another instance. Accepts integers from 1 to 2147483647; omitting it disables autoscaling for a new project. This is a per-instance threshold, not a project-wide build limit.

**Example**

```shell
# Create a project with default settings
depot projects create your-project-name

# Create a project with custom settings
depot projects create --organization your-org-id --region us-west-2 --cache-storage-policy 100 --cache-policy-keep-days 30 your-project-name

# Allow up to three concurrent builds per builder instance
depot projects create your-project-name --max-concurrent-builds 3
```

#### Flags for `projects create`

| Name                     | Description                                              |
| ------------------------ | -------------------------------------------------------- |
| `organization` / `-o`    | Depot organization ID                                    |
| `region`                 | Build data storage region (default: "us-east-1")         |
| `cache-storage-policy`   | Build cache to keep per architecture in GB (default: 50) |
| `cache-policy-keep-days` | Build cache retention in days (default: 14)              |
| `max-concurrent-builds` | Concurrent builds per builder instance before autoscaling (positive integer) |
| `token`                  | Depot API token                                          |

#### `depot projects get`

View a Depot project's details and configuration. Pass a project ID as an argument or with `--project-id`. If neither is provided, the command uses `DEPOT_PROJECT_ID` or the project configured in `depot.json`.

Text output includes **Builds per instance**, or **Disabled** when autoscaling is disabled. Project create, get, and update JSON output includes `max_concurrent_builds` as a number, or `null` when autoscaling is disabled.

**Example**

```shell
# View the project configured in the current directory
depot projects get

# View a specific project as JSON
depot projects get your-project-id --output json
```

#### Flags for `projects get`

| Name                | Description               |
| ------------------- | ------------------------- |
| `project-id` / `-p` | Depot project ID          |
| `output` / `-o`     | Output format (`json`)    |
| `token`             | Depot API token           |

#### `depot projects update`

Update a Depot project's name, region, cache storage policy, or autoscaling. Only values passed as flags are changed. Pass a project ID as an argument or with `--project-id`. If neither is provided, the command uses `DEPOT_PROJECT_ID` or the project configured in `depot.json`.

Set `--max-concurrent-builds` to a positive integer to enable or change autoscaling, or use `--disable-autoscaling` to remove the threshold. These flags are mutually exclusive. Omitting both preserves the existing autoscaling setting.

**Example**

```shell
# Rename the project configured in the current directory
depot projects update --name "New project name"

# Update a specific project's region and cache storage policy
depot projects update your-project-id --region eu-central-1 --cache-storage-policy 100

# Set the autoscaling threshold to three builds per builder instance
depot projects update your-project-id --max-concurrent-builds 3

# Disable project autoscaling
depot projects update your-project-id --disable-autoscaling
```

#### Flags for `projects update`

| Name                   | Description                                    |
| ---------------------- | ---------------------------------------------- |
| `project-id` / `-p`    | Depot project ID                               |
| `name`                 | Project name                                   |
| `region`               | Build data storage region                      |
| `cache-storage-policy` | Build cache to keep per architecture in GB     |
| `max-concurrent-builds` | Concurrent builds per builder instance before autoscaling (positive integer) |
| `disable-autoscaling` | Disable project autoscaling |
| `output` / `-o`        | Output format (`json`)                         |
| `token`                | Depot API token                                |

#### `depot projects delete`

Delete a Depot project. If no project ID is specified, the command will display an interactive list of projects to choose from.

**Example**

```shell
# Delete a specific project
depot projects delete --project-id your-project-id

# Delete a project interactively
depot projects delete

# Delete a project without confirmation prompt
depot projects delete --project-id your-project-id --yes
```

#### Flags for `projects delete`

| Name           | Description                                    |
| -------------- | ---------------------------------------------- |
| `project-id`   | The ID of the project to delete                |
| `yes`          | Confirm deletion without interactive prompt    |
| `token`        | Depot API token                                |

#### `depot projects list`

List Depot projects. This command is functionally identical to `depot list projects` and provides an interactive listing of your Depot projects.

**Example**

```shell
depot projects list
```

### `depot init`

Initialize an existing Depot project in the current directory. The CLI will display an interactive list of your Depot projects for you to choose from, then write a `depot.json` file in the current directory with the contents `{"projectID": "xxxxxxxxxx"}`.

**Example**

```
depot init
```

### `depot login`

Authenticates with your Depot account, automatically creating and storing a personal API token on your local machine.

**Example**

```shell
depot login
```

### `depot logout`

Remove any saved login details from your local machine.

**Example**

```shell
depot logout
```

### `depot org`

Manage Depot organizations. The `org` command consists of subcommands for each operation.

#### `depot org list`

Display an interactive listing of organizations that you can access. The command shows organization IDs and names in a table format.

To exit type `q` or `ctrl+c`

**Example**

```shell
depot org list
```

**Example**

The list command can output organization information to stdout with the `--output` option. It supports `json` and `csv`.

Output organizations in JSON format:

```shell
depot org list --output json
```

Output organizations in CSV format:

```shell
depot org list --output csv
```

#### `depot org show`

Show the current organization ID that is set in your global Depot settings.

**Example**

```shell
depot org show
```

#### `depot org switch`

Set the current organization in global Depot settings. You can either specify an organization ID as an argument or run the command interactively to select from available organizations.

**Example**

Switch to a specific organization by ID:

```shell
depot org switch your-org-id
```

**Example**

Switch to an organization interactively:

```shell
depot org switch
```

### `depot pull`

Pull an image from the Depot registry to your local Docker daemon.

```shell
depot pull --tag repo:tag <BUILD_ID>
```

Pull all bake images from the Depot registry to your local Docker daemon.
By default images will be tagged with the bake target names.

```shell
depot pull <BUILD_ID>
```

### `depot push`

Push an image from the Depot registry to a destination registry.
By default, it uses registry credentials stored in Docker when pushing to registries.
If you have not already authenticated with your registry, you can do so with `docker login`
before running `depot push`.

Alternatively, you can specify the environment variables `DEPOT_PUSH_REGISTRY_USERNAME` and `DEPOT_PUSH_REGISTRY_PASSWORD`
for the registry credentials. This allows you to skip the `docker login` step.

```shell
depot push --tag repo:tag <BUILD_ID>
```

### `depot pull-token`

Create a new pull token for the Depot registry. This command outputs a token string that can be used to authenticate with the Depot registry to pull images, which is useful in CI/CD environments or when direct authentication isn't possible.

**Example**

```shell
# Create a pull token for a specific build
depot pull-token <BUILD_ID>

# Create a pull token for a specific project
depot pull-token --project <PROJECT_ID>
```

#### Flags for `pull-token`

| Name      | Description      |
| --------- | ---------------- |
| `project` | Depot project ID |
| `token`   | Depot API token  |

### `depot version`

Display version information for the Depot CLI, including the version number, build date, and a link to the corresponding GitHub release.

**Example**

```shell
depot version
```

## Contributing

PR contributions are welcome! The CLI codebase is evolving rapidly, but we are happy to work with you on your contribution.

### Building from Source

To build the CLI from source, you'll need to have Go installed. You can then build the binary by running:

```shell
make bin/depot
```

This will create the `depot` executable at `bin/depot`.

## License

MIT License, see `LICENSE`
