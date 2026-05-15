# Developer Documentation

Documentation for developers and maintainers.

## Getting Started

1. [Development Guide](./development.md) — Local setup, build, and release
2. [Library Destinations Architecture](./destinations.md) — Multi-destination runtime model and data flow
3. [Screenshot Naming and Placeholders](./screenshot-naming.md) — Standard screenshot filenames and markdown placeholders

## Project Structure

Core packages:

- `cmd/server/` — Main web server entry point
- `internal/library/` — Sync, download, and destination fan-out logic
- `internal/mediaserver/` — Backend implementations (Plex/Emby/Jellyfin/ABS)
- `internal/audio/` — Download, decrypt, metadata enrichment (relies on go-audible)
- `internal/database/` — SQLite/Postgres schema and query layer
- `internal/web/` — HTTP handlers, templates, assets
- `internal/organizer/` — File organization and metadata writing
- `internal/scheduler/` — Cron-based library sync scheduling

## Dependencies

- [go-audible](https://github.com/mstrhakr/go-audible) — Audible API client and auth handler
- [Gin](https://github.com/gin-gonic/gin) — HTTP framework
- [ffmpeg](https://ffmpeg.org/) — Audio encoding/decoding (auto-downloaded if not on PATH)
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — Pure Go SQLite (no CGO needed)
