# `@depot/cli`

[![CI](https://github.com/depot/cli/actions/workflows/ci.yml/badge.svg)](https://github.com/depot/cli/actions/workflows/ci.yml)
[![npm](https://img.shields.io/npm/v/@depot/cli.svg)](https://www.npmjs.com/package/@depot/cli)
![Powered by TypeScript](https://img.shields.io/badge/powered%20by-typescript-blue.svg)

A Node.js package for downloading and interacting with the [Depot CLI](https://github.com/depot/cli).

## Installation

Use [pnpm](https://pnpm.io) or your favorite package manager:

```bash
pnpm add @depot/cli
```

## Usage

#### `depot(...)`

Call the Depot CLI with the given arguments - the `depot(...)` function accepts the same arguments as [execa](https://github.com/sindresorhus/execa), automatically injecting the Depot CLI binary as the first argument.

#### `depotBinaryPath()`

Returns the path to the Depot CLI binary.

### Example

```typescript
import {depot, depotBinaryPath} from '@depot/cli'

async function example() {
  console.log(depotBinaryPath())

  await depot('build', ['-t', 'org/repo:tag', '.'])
}
```

## License

MIT License, see `LICENSE`.

Code from ESBuild, copyright (c) 2020 Evan Wallace
