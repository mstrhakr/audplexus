#!/bin/bash
# Local Docker build script
# This script prepares the build context for Docker with the go-audible dependency

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_RELEASE_VERSION="$(git -C "$SCRIPT_DIR" tag --sort=-version:refname | sed -n '1s/^v//p')"
if [ -z "$BUILD_RELEASE_VERSION" ]; then
    BUILD_RELEASE_VERSION="0.0.0"
fi
BUILD_COMMIT_REF="$(git -C "$SCRIPT_DIR" rev-parse --short=12 HEAD 2>/dev/null || true)"
BUILD_TIMESTAMP="$(date -u +%Y%m%d%H%M%S)"
BUILD_CHANNEL="dev"
BUILD_DIR="$(mktemp -d)"

echo "Preparing Docker build context in $BUILD_DIR..."

# Copy audplexus
echo "Copying audplexus..."
cp -r "$SCRIPT_DIR" "$BUILD_DIR/audplexus"

# Copy or clone go-audible
if [ -d "$SCRIPT_DIR/../go-audible" ]; then
    echo "Copying local go-audible from ../go-audible..."
    cp -r "$SCRIPT_DIR/../go-audible" "$BUILD_DIR/go-audible"
else
    echo "Cloning go-audible from GitHub..."
    git clone https://github.com/mstrhakr/go-audible.git "$BUILD_DIR/go-audible"
fi

# Build the Docker image
echo "Building Docker image..."
cd "$BUILD_DIR"
docker build \
    --build-arg BUILD_RELEASE_VERSION="$BUILD_RELEASE_VERSION" \
    --build-arg BUILD_COMMIT_REF="$BUILD_COMMIT_REF" \
    --build-arg BUILD_TIMESTAMP="$BUILD_TIMESTAMP" \
    --build-arg BUILD_CHANNEL="$BUILD_CHANNEL" \
    -f audplexus/Dockerfile -t audplexus:local .

echo "Cleaning up build context..."
rm -rf "$BUILD_DIR"

echo ""
echo "✅ Docker image built successfully as 'audplexus:local'"
echo "Version stamp: $BUILD_RELEASE_VERSION.${BUILD_COMMIT_REF:-$BUILD_TIMESTAMP}-dev"
echo ""
echo "To run:"
echo "  docker run -d -p 8080:8080 -v ./config:/config -v ./audiobooks:/audiobooks audplexus:local"

