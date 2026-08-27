# Development Guide

This guide covers the practical workflow for working on Audplexus locally, shipping changes safely, and understanding the project layout.

## Project overview

Audplexus is a self-hosted audiobook automation app that:

- connects to Audible and syncs your library
- downloads and decrypts audiobooks
- normalizes metadata and file layout
- pushes completed books to media-server destinations
- exposes status, diagnostics, and configuration through a web UI

The main runtime entry points are:

- `cmd/server/` — application bootstrap and server startup
- `internal/web/` — HTTP handlers, templates, and UI logic
- `internal/library/` — library sync, download orchestration, and destination fan-out
- `internal/mediaserver/` — backend integrations for Plex, Emby, Jellyfin, and Audiobookshelf
- `internal/database/` — schema, migrations, and persistence
- `internal/organizer/` — file organization and audiobook metadata handling
- `internal/audio/` — media processing and metadata enrichment
- `internal/auth/` and `internal/config/` — auth and app configuration

## Local development requirements

Before working locally, make sure you have:

- Go 1.22+
- a checkout of this repository
- a sibling checkout of `go-audible` for local module work
- `ffmpeg` available on `PATH` for media conversion/inspection tasks
- Docker if you want to validate the container deployment path

The project uses pure Go SQLite via `modernc.org/sqlite`, so CGO is not required in normal local development.

## One-time local setup

From a parent directory that contains both repos:

```bash
git clone https://github.com/mstrhakr/audplexus.git
git clone https://github.com/mstrhakr/go-audible.git
```

Then build the server locally:

```bash
cd audplexus
go build -o audplexus ./cmd/server
./audplexus
```

If you are testing the Docker workflow, use the project helper scripts instead of hand-assembling the build context:

```bash
# Linux/macOS
./build-docker.sh

# Windows PowerShell
./build-docker.ps1
```

These helpers assemble the correct local build context for both repositories and produce `audplexus:local`.

## Standard development commands

Run the full Go test suite:

```bash
go test ./... -count=1
```

Run a focused package test when iterating quickly:

```bash
go test ./internal/library -count=1
go test ./internal/web -count=1
go test ./cmd/server -count=1
```

Build the server binary:

```bash
go build -o audplexus ./cmd/server
```

Start the app using Docker Compose if you want the containerized path:

```bash
docker compose up -d
```

## Testing expectations

- write or update tests alongside real behavior changes
- prefer focused tests for the package you changed
- avoid mock-heavy tests that assert implementation internals instead of actual outcomes
- keep migrations and data model changes covered by real database tests when possible

A good rule: if a bug involved data drift, library state, retry logic, or destination fan-out, test that behavior at the package boundary rather than only at a controller layer.

## Repository conventions

### Go code

- keep logic in the appropriate package; do not spread orchestration across unrelated packages
- propagate `context.Context` through network and long-running operations
- treat DB schema changes as real product changes and verify migration safety
- prefer small, readable commits that match a single behavior change

### Docs and architecture notes

- keep docs close to the subsystem they describe
- use lowercase filenames for docs unless there is a strong reason otherwise
- keep design and operational notes grounded in the current codebase, not in stale assumptions

### Releases and packaging

The release process is defined in the repo scripts and CI, and should not be edited by hand. The important distinction is:

- version bumping is automated via the release scripts
- Docker and binary publishing happen through the standard release path

## Release tags and artifacts

When a release is tagged, CI publishes Docker tags such as:

- exact: `v0.1.4`, `0.1.4`
- floating minor: `v0.1`, `0.1`
- floating major: `v0`, `0`
- latest: `latest`
- branch: `master`, `main`
- commit-specific: `master-sha-<shortsha>`

Example:

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

## Release flow

Create and push a release tag:

```bash
git commit --allow-empty -m "chore: release v0.1.4"
git tag v0.1.4
git push origin v0.1.4
```

CI then handles:

- building binaries for Linux, macOS, and Windows (amd64 + arm64)
- creating a GitHub release with archives
- building and publishing Docker images with floating and exact tags

Releases: <https://github.com/mstrhakr/audplexus/releases>

## Troubleshooting

### `go: module ... not found`

This usually means the sibling `go-audible` checkout is not in the expected location or the local module replacement is not set up correctly. Ensure the repo is checked out next to Audplexus and the project’s local build flow is used.

### `ffmpeg` missing or commands fail

Make sure `ffmpeg` is installed and available on `PATH` before running audio-related workflows.

### app boots but library jobs or media-server syncs fail

Check the relevant package logs and diagnostics output first. The common causes are:

- invalid destination config
- permissions issues on the audiobook output path
- stale or partial state from a prior failed sync
- auth expiry for Audible or a media-server backend

### state feels stale after a failed run

Try clearing the app data and rerunning a focused validation path with the logs enabled. Repeated failures after a clean state often point to a real configuration or auth issue rather than a corrupt runtime state.

### Docker path behaves differently from local

Use the provided helper scripts for local builds and keep the runtime paths, config directory, and mount permissions aligned with the Docker docs. The repo includes Docker-first install expectations that are easier to trust than ad hoc container runs.

## Contributing guide

When making changes:

- keep fixes rooted in the underlying cause, not in symptom-level workarounds
- document architecture changes in the dev docs when the behavior is no longer obvious
- prefer tests that validate real user-facing outcomes
- update docs when config, behavior, or environment assumptions change

The project has strong packaging and release automation, so the levers that matter most are correctness, reliability, and clear operational diagnostics.

## Related docs

- [Developer Documentation Index](./README.md)
- [1.0 Roadmap](./1.0-roadmap.md)
- [Diagnostic Proxy Worker](./diagnostic-proxy-worker.md)
- [Library Destinations Architecture](./destinations.md)
- [Screenshot Naming and Placeholders](./screenshot-naming.md)
