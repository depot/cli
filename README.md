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
    - [`depot cache`](#depot-cache)
      - [`depot cache reset`](#depot-cache-reset)
    - [`depot configure-docker`](#depot-configure-docker)
    - [`depot exec`](#depot-exec)
    - [`depot gocache`](#depot-gocache)
    - [`depot init`](#depot-init)
    - [`depot list`](#depot-list)
      - [`depot list projects`](#depot-list-projects)
      - [`depot list builds`](#depot-list-builds)
    - [`depot login`](#depot-login)
    - [`depot logout`](#depot-logout)
    - [`depot projects`](#depot-projects)
      - [`depot projects create`](#depot-projects-create)
      - [`depot projects list`](#depot-projects-list)
    - [`depot pull`](#depot-pull)
    - [`depot pull-token`](#depot-pull-token)
    - [`depot push`](#depot-push)
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

For all other platforms, you can download the binary directly from [the latest release](https://github.com/depot/cli/releases).

## Quick Start

1. Run `depot login` to authenticate with your Depot account.
2. `cd` to your project directory.
3. Run `depot init` to link the local directory with a Depot project - this will create a `depot.json` file in the current directory.
4. Run `depot build -t repo/image:tag .`

## Usage

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
| `save`           | Saves bake targets to the Depot registry                                                        |
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
| `provenance`      | Shortand for "--attest=type=provenance"                                                                   |
| `pull`            | Always attempt to pull all referenced images                                                              |
| `push`            | Shorthand for "--output=type=registry"                                                                    |
| `quiet`           | Suppress the build output and print image ID on success                                                   |
| `save`           | Saves build to the Depot registry                                                                |
| `sbom`            | Shorthand for "--attest=type=sbom"                                                                        |
| `secret`          | Secret to expose to the build (format: "id=mysecret[,src=/local/secret]")                                 |
| `shm-size`        | Size of "/dev/shm"                                                                                        |
| `ssh`             | SSH agent socket or keys to expose to the build                                                           |
| `tag`             | Name and optionally a tag (format: "name:tag")                                                            |
| `target`          | Set the target build stage to build                                                                       |
| `token`           | Depot API token                                                                                           |
| `ulimit`          | Ulimit options (default [])                                                                               |

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

### `depot configure-docker`

Configure Docker to use Depot's remote builder infrastructure. This command installs Depot as a Docker CLI plugin (i.e., `docker depot ...`) and sets the Depot plugin as the default Docker builder (i.e., `docker build`).

```shell
depot configure-docker
```

If you want to uninstall the plugin, you can specify the `--uninstall` flag.

```shell
depot configure-docker --uninstall
```

### `depot exec`

Execute a command with injected BuildKit connection. This allows running commands that need access to BuildKit on a remote Depot machine.

**Flags**

| Name          | Default          | Description                                                                        |
| ------------- | ---------------- | ---------------------------------------------------------------------------------- |
| `--env-var`   | `BUILDKIT_HOST`  | Environment variable name that will contain the BuildKit connection address        |
| `--platform`  | (auto-detected)  | Platform to execute the command on (`linux/amd64` or `linux/arm64`)               |
| `--project`   | (interactive)    | Depot project ID                                                                   |
| `--progress`  | `auto`           | Set type of progress output (options: `auto`, `plain`, `tty`)                      |
| `--token`     |                  | Depot API token (if not provided, will use logged-in token)                        |

**Examples**

```shell
# Running a Docker BuildX command with Depot
depot exec -- docker buildx build --platform linux/amd64,linux/arm64 -t myorg/myimage:latest .

# Running Dagger with Depot
depot exec -- dagger do build

# Specifying a project and platform
depot exec --project my-project-id --platform linux/arm64 -- docker build .
```

### `depot gocache`

Configure Go tools to use Depot Cache. The Go tools will use the remote cache service to store and retrieve build artifacts.

_Note: This requires Go 1.24 or later._


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

### `depot init`

Initialize an existing Depot project in the current directory. The CLI will display an interactive list of your Depot projects for you to choose from, then write a `depot.json` file in the current directory with the contents `{"projectID": "xxxxxxxxxx"}`.

**Example**

```
depot init
```

### `depot list`

Interact with Depot projects and builds.

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

### `depot projects`

Create or display information about Depot projects.

**Aliases:** `p`

#### `depot projects create`

Creates a new Depot project.

**Usage**

```shell
depot projects create [flags] <project-name>
```

**Aliases:** `c`

**Flags**

| Name                  | Description                                           |
| --------------------- | ----------------------------------------------------- |
| `--token`             | Depot API token                                       |
| `--organization`, `-o`| Depot organization ID                                 |
| `--region`            | Region for build data storage (default: "us-east-1")  |
| `--cache-storage-policy` | Build cache size per architecture in GB (default: 50) |

**Examples**

```shell
# Create a basic project
depot projects create my-new-project

# Create a project in a specific organization and region
depot projects create my-org-project --organization org-123 --region eu-west-1

# Create a project with larger cache size
depot projects create large-cache-project --cache-storage-policy 100
```

#### `depot projects list`

Lists all Depot projects available to the authenticated user.

**Usage**

```shell
depot projects list [flags]
```

**Aliases:** `ls`

**Flags**

| Name       | Description                                              |
| ---------- | -------------------------------------------------------- |
| `--token`  | Depot API token                                          |
| `--output` | Non-interactive output format (options: json, csv)       |

**Examples**

```shell
# List projects interactively
depot projects list

# Export project list to JSON
depot projects list --output=json

# Export project list to CSV
depot projects list --output=csv > projects.csv
```

### `depot pull`

Pull an image from the Depot registry to your local Docker daemon.

**Usage**

```shell
depot pull [flags] [buildID]
```

**Flags**

| Name              | Description                                                                        |
| ----------------- | ---------------------------------------------------------------------------------- |
| `--project`       | Depot project ID                                                                   |
| `--token`         | Depot token                                                                        |
| `--platform`      | Pull image for a specific platform ("linux/amd64", "linux/arm64")                  |
| `-t, --tag`       | Optional tags to apply to the image when pulled locally (can specify multiple)     |
| `--progress`      | Set type of progress output ("auto", "plain", "tty", "quiet") (default "auto")     |
| `--target`        | Pull image for specific bake targets (only applicable for bake builds)             |

**Examples**

```shell
# Pull a specific build by ID
depot pull abc123

# Pull a build and tag it locally
depot pull --tag repo:tag <BUILD_ID>

# Pull all bake images from a build
depot pull <BUILD_ID>

# Pull a specific bake target from a build
depot pull abc123 --target frontend

# Pull a specific platform
depot pull abc123 --platform linux/arm64
```

### `depot pull-token`

Create a new pull token for the Depot registry.

**Usage**

```shell
depot pull-token [flags] ([buildID])
```

**Flags**

| Name         | Description                                            |
| ------------ | ------------------------------------------------------ |
| `--project`  | Depot project ID (required if no build ID provided)    |
| `--token`    | Depot API token                                        |

**Examples**

```shell
# Generate a pull token for a specific build
depot pull-token abc123def456

# Generate a pull token for a specific project
depot pull-token --project proj_123456

# Use with Docker login
docker login registry.depot.dev -u x-token -p $(depot pull-token)
```

### `depot push`

Push an image from the Depot registry to a destination registry.

**Usage**

```shell
depot push [flags] [buildID]
```

**Flags**

| Name              | Description                                                                     |
| ----------------- | ------------------------------------------------------------------------------- |
| `--project`       | Depot project ID                                                                |
| `--token`         | Depot token                                                                     |
| `--progress`      | Set type of progress output ("auto", "plain", "tty", "quiet") (default "auto")  |
| `-t, --tag`       | Name and tag for the pushed image (format: "name:tag") - **REQUIRED**           |
| `--target`        | Bake target to push (only needed for bake builds with multiple targets)         |

**Examples**

```shell
# Push a specific build to Docker Hub
depot push abc123 --tag username/repo:latest

# Push to GitHub Container Registry
depot push abc123 --tag ghcr.io/username/repo:v1.0.0

# Push a specific bake target
depot push abc123 --tag username/repo:latest --target backend

# Push to multiple registries/tags
depot push abc123 --tag username/repo:latest --tag username/repo:v1.0.0
```

## Contributing

PR contributions are welcome! The CLI codebase is evolving rapidly, but we are happy to work with you on your contribution.

## License

MIT License, see `LICENSE`