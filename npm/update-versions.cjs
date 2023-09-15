const fs = require('fs')

const pkg = require('./packages/cli/package.json')
const packageVersion = pkg.version.split('-cli.')[0]

const DEPOT_CLI_VERSION = process.env.DEPOT_CLI_VERSION?.replace('v', '')
if (!DEPOT_CLI_VERSION) throw new Error('Missing DEPOT_CLI_VERSION')

const version = `${packageVersion}-cli.${DEPOT_CLI_VERSION}`

const packages = fs.readdirSync('./packages')

for (const name of packages) {
  console.log('name', name)
  const pkgPath = `./packages/${name}/package.json`
  const pkg = require(pkgPath)

  pkg.version = version

  if (name === 'cli') {
    const optionalDependencies = {}
    for (const name of Object.keys(pkg.optionalDependencies)) {
      optionalDependencies[name] = version
    }
    pkg.optionalDependencies = optionalDependencies
  }

  fs.writeFileSync(pkgPath, JSON.stringify(pkg, null, 2) + '\n')
}
