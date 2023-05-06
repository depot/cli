# Depot CLI [![CI](https://github.com/depot/cli/actions/workflows/ci.yml/badge.svg)](https://github.com/depot/cli/actions/workflows/ci.yml)

Official CLI for [Depot](https://depot.dev) - you can use the CLI to build Docker images via Depot's remote builder infrastructure.

---

> ⚡ **Depot** provides cloud-hosted container builder machines - our builders are quick, running on native hardware. Build caching is fully managed with no extra configuration.
>
> [More information →](https://depot.dev)

---

- [Installation](#installation)
- [Quick Start](#quick-start)
- [Usage](#usage)
  - [`depot bake`](#depot-bake)
  - [`depot build`](#depot-build)
  - [`depot cache`](#depot-cache)
    - [`depot cache reset`](#depot-cache-reset)
  - [`depot list`](#depot-list)
    - [`depot list projects`](#depot-list-projects)
    - [`depot list builds`](#depot-list-builds)
  - [`depot init`](#depot-init)
  - [`depot login`](#depot-login)
- [Contributing](#contributing)
- [License](#license)

## Installation

For Mac, you can install the CLI with Homebrew:

```
brew install depot/tap/depot
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

The `bake` command needs to know which [project](https://depot.dev/docs/core-concepts#projects) id to route the build to. For passing the project id you have three options available to you:

1. Run `depot init` at the root of your repository and commit the resulting `depot.json` file
2. Use the `--project` flag in your `depot bake` command
3. Set the `DEPOT_PROJECT_ID` environment variable which will be automatically detected

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

## Contributing

PR contributions are welcome! The CLI codebase is evolving rapidly, but we are happy to work with you on your contribution.

## License

MIT License, see `LICENSE`
