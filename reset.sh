#!/usr/bin/env bash

set -e

make
docker buildx build -t ghcr.io/depot/cli:local .
docker stop buildx_buildkit_depot-project0 2>/dev/null || true
