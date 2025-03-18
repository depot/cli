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
    - [`depot gocache`](#depot-gocache)
    - [`depot configure-docker`](#depot-configure-docker)
    - [`depot list`](#depot-list)
      - [`depot list projects`](#depot-list-projects)
      - [`depot list builds`](#depot-list-builds)
    - [`depot init`](#depot-init)
    - [`depot login`](#depot-login)
    - [`depot logout`](#depot-logout)
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

### `depot configure-docker`

Configure Docker to use Depot's remote builder infrastructure. This command installs Depot as a Docker CLI plugin (i.e., `docker depot ...`) and sets the Depot plugin as the default Docker builder (i.e., `docker build`).

```shell
depot configure-docker
```

If you want to uninstall the plugin, you can specify the `--uninstall` flag.

```shell
depot configure-docker --uninstall
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

Remove any saved login defails from your local machine.

**Example**

```shell
depot logout
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

```shell
depot push --tag repo:tag <BUILD_ID>
```

## Contributing

PR contributions are welcome! The CLI codebase is evolving rapidly, but we are happy to work with you on your contribution.

## License

MIT License, see `LICENSE`
