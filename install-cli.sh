#!/bin/sh
# Based on Deno installer: Copyright 2019 the Deno authors. All rights reserved. MIT license.

set -e

os=$(uname -s)
arch=$(uname -m)
version=${1:-latest}

release_url=$(curl --silent --write-out "%{redirect_url}\n" --output /dev/null "https://dl.depot.dev/cli/download/$os/$arch/$version")
if [ ! "$release_url" ]; then
  echo "Error: Unable to find a Depot CLI release for $os/$arch/$version - see https://github.com/depot/cli/releases for all versions" 1>&2
  exit 1
fi

install_dir="${DEPOT_INSTALL_DIR:-$HOME/.depot/bin}"
bin="$install_dir/depot"

if [ ! -d "$install_dir" ]; then
  mkdir -p "$install_dir"
fi

extract_dir="$(mktemp -d)"
curl -q --fail --location --progress-bar --output "$extract_dir/depot.tar.gz" "$release_url"
cd "$extract_dir"
tar xzf "depot.tar.gz"
mv "$extract_dir/bin/depot" "$bin"
chmod +x "$bin"
rm -rf "$extract_dir"

echo "Depot CLI was installed successfully to $bin"
if command -v depot >/dev/null; then
  echo "Run 'depot --help' to get started"
else
  case $SHELL in
  /bin/zsh) shell_profile=".zshrc" ;;
  *) shell_profile=".bash_profile" ;;
  esac
  echo "Manually add the directory to your \$HOME/$shell_profile (or similar)"
  echo "  export DEPOT_INSTALL_DIR=\"$install_dir\""
  echo "  export PATH=\"\$DEPOT_INSTALL_DIR:\$PATH\""
  echo "Run '$bin --help' to get started"
fi
