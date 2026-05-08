# Development Guide

This document contains developer and maintainer information for Audplexus.

## Local Development

Requirements:

- Go 1.22+
- A checkout of this repository
- A sibling checkout of `go-audible` when doing local module work

Build and run locally:

```bash
go build -o audplexus ./cmd/server
./audplexus
```

The project uses pure Go SQLite (`modernc.org/sqlite`), so CGO is not required.

## Build Docker Image Locally

Because this project depends on a local `go-audible` module, use the provided helper scripts:

```bash
# Linux/macOS
./build-docker.sh

# Windows PowerShell
./build-docker.ps1
```

These scripts assemble the correct build context for both repositories and produce `audplexus:local`.

## Docker Release Tags

When a release is tagged (for example `v0.1.4`), CI publishes these image tags:

- Exact: `v0.1.4`, `0.1.4`
- Floating minor: `v0.1`, `0.1`
- Floating major: `v0`, `0`
- Latest: `latest`
- Branch: `master`, `main`
- Commit specific: `master-sha-<shortsha>`

Examples:

```bash
# Latest stable
docker pull ghcr.io/mstrhakr/audplexus:latest

# Floating major
docker pull ghcr.io/mstrhakr/audplexus:v0

# Floating minor
docker pull ghcr.io/mstrhakr/audplexus:v0.1

# Exact version
docker pull ghcr.io/mstrhakr/audplexus:v0.1.4
```

## Release Process

Create and push a release tag:

```bash
git commit --allow-empty -m "chore: release v0.1.4"
git tag v0.1.4
git push origin v0.1.4
```

CI will then:

- Build binaries for Linux, macOS, and Windows (amd64 + arm64)
- Create a GitHub Release with archives
- Build and publish Docker images with floating and exact tags

Releases: <https://github.com/mstrhakr/audplexus/releases>
