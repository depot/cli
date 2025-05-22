#!/bin/bash
set -e

echo "Building test binaries for all platforms..."

# Clean previous builds
rm -rf test-binaries
mkdir -p test-binaries

# Build for each platform
echo "Building Linux amd64..."
GOOS=linux GOARCH=amd64 go build -o test-binaries/depot-test-linux-amd64 ./cmd/depot

echo "Building Linux arm64..."
GOOS=linux GOARCH=arm64 go build -o test-binaries/depot-test-linux-arm64 ./cmd/depot

echo "Building Windows amd64..."
GOOS=windows GOARCH=amd64 go build -o test-binaries/depot-test-windows-amd64.exe ./cmd/depot

echo "Building macOS amd64..."
GOOS=darwin GOARCH=amd64 go build -o test-binaries/depot-test-darwin-amd64 ./cmd/depot

echo "Building macOS arm64..."
GOOS=darwin GOARCH=arm64 go build -o test-binaries/depot-test-darwin-arm64 ./cmd/depot

# Also build the standalone detection test
echo "Building detection test binaries..."
GOOS=linux GOARCH=amd64 go build -o test-binaries/test-detection-linux-amd64 ./cmd/test-detection
GOOS=windows GOARCH=amd64 go build -o test-binaries/test-detection-windows-amd64.exe ./cmd/test-detection
GOOS=darwin GOARCH=amd64 go build -o test-binaries/test-detection-darwin-amd64 ./cmd/test-detection
GOOS=darwin GOARCH=arm64 go build -o test-binaries/test-detection-darwin-arm64 ./cmd/test-detection

echo "Done! Binaries are in test-binaries/"
ls -la test-binaries/