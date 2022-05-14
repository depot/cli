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
  - [`depot build`](#depot-build)
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

### `depot build`

Runs a Docker build using Depot's remote builder infrastructure. This command accepts all the command line flags as Docker's `docker buildx build` command, you can run `depot build --help` for the full list.

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

### `depot init`

Initialize an existing Depot project in the current directory. The CLI will display an interactive list of your Depot projects for you to choose from, then write a `depot.json` file in the current directory with the contents `{"projectID": "xxxxxxxxxx"}`.

**Example**

```
depot init
```

### `depot login`

Authenticates with your Depot account, automatically creating and storing a personal API token on your local machine.

**Example**

```
depot login
```

## Contributing

PR contributions are welcome! The CLI codebase is evolving rapidly, but we are happy to work with you on your contribution.

## License

MIT License, see `LICENSE`
