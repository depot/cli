import {BufferEncodingOption, ExecaChildProcess, Options, execa} from 'execa'
import * as path from 'path'
import {DEPOT_BINARY_PATH, generateBinPath} from './platform'

export function depotBinaryPath(): string {
  // Try to have a nice error message when people accidentally bundle @depot/cli
  // without providing an explicit path to the binary.
  if (!DEPOT_BINARY_PATH && (path.basename(__filename) !== 'main.js' || path.basename(__dirname) !== 'lib')) {
    throw new Error(
      `The Depot CLI cannot be bundled. Please mark the "@depot/cli" ` +
        `package as external so it's not included in the bundle.\n` +
        `\n` +
        `More information: The file containing the code for depot's JavaScript ` +
        `API (${__filename}) does not appear to be inside the @depot/cli package on ` +
        `the file system, which usually means that the @depot/cli package was bundled ` +
        `into another file. This is problematic because the API needs to run a ` +
        `binary executable inside the @depot/cli package which is located using a ` +
        `relative path from the API code to the executable. If the @depot/cli package ` +
        `is bundled, the relative path will be incorrect and the executable won't ` +
        `be found.`,
    )
  }

  const {binPath} = generateBinPath()
  return binPath
}

export function depot(args?: readonly string[], options?: Options): ExecaChildProcess
export function depot(args?: readonly string[], options?: Options<BufferEncodingOption>): ExecaChildProcess<Buffer>
export function depot(options?: Options): ExecaChildProcess
export function depot(options?: Options<BufferEncodingOption>): ExecaChildProcess<Buffer>
export function depot(...args: any[]): any {
  const bin = depotBinaryPath()
  return execa(bin, ...args)
}
