#!/bin/bash
set -ex

node bundle.cjs
pnpm generate-types

chmod +x packages/cli/bin/depot

mkdir -p packages/cli-linux-ia32/bin/
mkdir -p packages/cli-linux-x64/bin/
mkdir -p packages/cli-linux-arm/bin/
mkdir -p packages/cli-linux-arm64/bin/
mkdir -p packages/cli-darwin-x64/bin/
mkdir -p packages/cli-darwin-arm64/bin/
mkdir -p packages/cli-win32-ia32/bin/
mkdir -p packages/cli-win32-x64/bin/
mkdir -p packages/cli-win32-arm/bin/
mkdir -p packages/cli-win32-arm64/bin/

cp ../dist/linux_linux_386/bin/depot packages/cli-linux-ia32/bin/
cp ../dist/linux_linux_amd64_v1/bin/depot packages/cli-linux-x64/bin/
cp ../dist/linux_linux_arm_6/bin/depot packages/cli-linux-arm/bin/
cp ../dist/linux_linux_arm64/bin/depot packages/cli-linux-arm64/bin/
cp ../dist/macos_darwin_amd64_v1/bin/depot packages/cli-darwin-x64/bin/
cp ../dist/macos_darwin_arm64/bin/depot packages/cli-darwin-arm64/bin/
cp ../dist/windows_windows_386/bin/depot.exe packages/cli-win32-ia32/bin/
cp ../dist/windows_windows_amd64_v1/bin/depot.exe packages/cli-win32-x64/bin/
cp ../dist/windows_windows_arm_6/bin/depot.exe packages/cli-win32-arm/bin/
cp ../dist/windows_windows_arm64/bin/depot.exe packages/cli-win32-arm64/bin/
