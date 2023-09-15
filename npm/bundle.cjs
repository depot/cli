const esbuild = require('esbuild')
const pkg = require('./packages/cli/package.json')

const DEPOT_CLI_VERSION = pkg.version.split('-cli.')[1]

// packages/cli/install.js
esbuild.build({
  entryPoints: ['src/install.ts'],
  bundle: true,
  platform: 'node',
  target: 'node14',
  outfile: 'packages/cli/install.js',
  external: ['@depot/cli'],
})

// packages/cli/lib/main.js
esbuild.build({
  entryPoints: ['src/main.ts'],
  bundle: true,
  platform: 'node',
  target: 'node14',
  outfile: 'packages/cli/lib/main.js',
  external: ['@depot/cli'],
  define: {DEPOT_CLI_VERSION: JSON.stringify(DEPOT_CLI_VERSION)},
})

// packages/cli/bin/depot
esbuild.build({
  entryPoints: ['src/shim.ts'],
  bundle: true,
  platform: 'node',
  target: 'node14',
  outfile: 'packages/cli/bin/depot',
  external: ['@depot/cli'],
  define: {DEPOT_CLI_VERSION: JSON.stringify(DEPOT_CLI_VERSION)},
})
