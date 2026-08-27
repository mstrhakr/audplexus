# Developer Documentation

This is the maintainer and contributor documentation for Audplexus. It covers the build workflow, project structure, architecture notes, diagnostics tooling, and release process.

## Getting Started

1. [Development Guide](./development.md) — Local setup, build flow, testing expectations, release process, and contributor standards
2. [1.0 Roadmap](./1.0-roadmap.md) — Release plan, milestones, and stability gates for a 1.0 launch
3. [Diagnostic Proxy Worker](./diagnostic-proxy-worker.md) — Cloudflare Worker for diagnostics handoff and GitHub issue prefill
4. [Library Destinations Architecture](./destinations.md) — Multi-destination runtime model and data flow
5. [Screenshot Naming and Placeholders](./screenshot-naming.md) — Standard screenshot filenames and markdown placeholders

## Project Structure

Core packages:

- `cmd/server/` — main web server entry point
- `internal/library/` — library sync, download orchestration, and destination fan-out
- `internal/mediaserver/` — backend integrations for Plex, Emby, Jellyfin, and Audiobookshelf
- `internal/audio/` — download, decryption, and metadata/enrichment work
- `internal/database/` — schema, migrations, and persistence layer
- `internal/web/` — HTTP handlers, templates, and UI logic
- `internal/organizer/` — metadata-aware file organization and naming
- `internal/scheduler/` — scheduled sync and background flow management

## Dependencies

- [go-audible](https://github.com/mstrhakr/go-audible) — Audible API client and auth layer
- [Gin](https://github.com/gin-gonic/gin) — HTTP framework
- [ffmpeg](https://ffmpeg.org/) — media conversion and inspection tooling
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — pure Go SQLite, no CGO required

## Contributor guidance

- start with the [Development Guide](./development.md) for local setup and project habits
- use the architecture notes when changing destination behavior, auth, or library flow
- keep docs current when behavior changes materially
- prefer real-world regression tests for DB, sync, and destination logic
