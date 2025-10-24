const esbuild = require('esbuild')

const DEPOT_CLI_VERSION = process.env.DEPOT_CLI_VERSION?.replace('v', '')
if (!DEPOT_CLI_VERSION) throw new Error('Missing DEPOT_CLI_VERSION')

// packages/cli/install.js
esbuild.build({
  entryPoints: ['src/install.ts'],
  bundle: true,
  platform: 'node',
  target: 'node20',
  outfile: 'packages/cli/install.js',
  external: ['@depot/cli'],
})

// packages/cli/lib/main.js
esbuild.build({
  entryPoints: ['src/main.ts'],
  bundle: true,
  platform: 'node',
  target: 'node20',
  outfile: 'packages/cli/lib/main.js',
  external: ['@depot/cli'],
  define: {DEPOT_CLI_VERSION: JSON.stringify(DEPOT_CLI_VERSION)},
})

// packages/cli/bin/depot
esbuild.build({
  entryPoints: ['src/shim.ts'],
  bundle: true,
  platform: 'node',
  target: 'node20',
  outfile: 'packages/cli/bin/depot',
  external: ['@depot/cli'],
  define: {DEPOT_CLI_VERSION: JSON.stringify(DEPOT_CLI_VERSION)},
})
