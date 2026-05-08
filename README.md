# Audplexus

![Audplexus Logo](logo.svg)

A self-hosted web app that syncs your Audible library, downloads and processes audiobooks, and organizes output for Plex audiobook libraries.

## What It Does

- Connects to Audible and syncs your library metadata.
- Downloads books and processes them into Plex-friendly output.
- Adds metadata and optional companion files (cover, chapters, Plex match hints).
- Supports queue controls, retries, diagnostics, and scheduled sync.

## Quick Start

### Docker Compose (recommended)

1. Create folders:

```bash
mkdir -p config audiobooks downloads
```

1. Start Audplexus:

```bash
docker compose up -d
```

1. Open the web UI:

`http://localhost:8080`

1. In Settings, connect Audible and (optionally) Plex.

### Docker Run

```bash
docker pull ghcr.io/mstrhakr/audplexus:latest

mkdir -p config audiobooks downloads

docker run -d \
  --name audible-plex \
  -p 8080:8080 \
  --user 1000:1000 \
  -v $(pwd)/config:/config \
  -v $(pwd)/audiobooks:/audiobooks \
  -v $(pwd)/downloads:/downloads \
  ghcr.io/mstrhakr/audplexus:latest
```

## Configuration

You can configure Audplexus with:

- Settings saved in the web UI (highest priority)
- Environment variables
- `config.yaml` defaults

Key environment variables:

| Variable | Default | Description |
| --- | --- | --- |
| `DATABASE_TYPE` | `sqlite` | Database backend (`sqlite` or `postgres`) |
| `DATABASE_PATH` | `/config/audible.db` | SQLite database path |
| `DATABASE_DSN` | | PostgreSQL connection string |
| `AUDIOBOOKS_PATH` | `/audiobooks` | Output directory (Plex library root) |
| `DOWNLOADS_PATH` | `/downloads` | Temporary download directory |
| `CONFIG_PATH` | `/config` | Config/auth storage directory |
| `OUTPUT_FORMAT` | `m4b` | Output format (`m4b` or `mp3`) |
| `DOWNLOAD_CONCURRENCY` | `0` | Concurrent downloads (0 = auto-detect based on CPU) |
| `DECRYPT_CONCURRENCY` | `0` | Concurrent decrypt workers (0 = auto-detect) |
| `PROCESS_CONCURRENCY` | `0` | Concurrent process workers (0 = auto-detect) |
| `PLEX_URL` | | Plex server URL for library scan triggers |
| `PLEX_TOKEN` | | Plex authentication token |
| `SYNC_SCHEDULE` | `0 */6 * * *` | Cron schedule for library sync |
| `SYNC_MODE` | `full` | Scheduled sync mode (`quick` or `full`) |
| `PUID` | | Unraid-style runtime UID override (used when container starts as root) |
| `PGID` | | Unraid-style runtime GID override (used when container starts as root) |
| `TAKE_OWNERSHIP` | `false` | If `true`, recursively `chown`s mounted dirs on startup before dropping privileges |

For full examples, see `config.example.yaml`.

## Storage Layout

Expected output structure:

```text
/audiobooks/
  Author Name/
    Book Title/
      Book Title.m4b
      Book Title.chapters.txt
      cover.jpg
```

## Permissions Notes

When a filesystem permission error occurs (for example writing to `/downloads` or moving into `/audiobooks`), queue workers are paused automatically to prevent repeated failures.

After fixing permissions, resume queue processing from the Pipeline page.

### User/Group Mapping

Use either standard Docker user mapping or Unraid-style `PUID`/`PGID`.

Standard Docker style:

```bash
docker run -d \
  --name audible-plex \
  --user 1000:1000 \
  -p 8080:8080 \
  -v $(pwd)/config:/config \
  -v $(pwd)/audiobooks:/audiobooks \
  -v $(pwd)/downloads:/downloads \
  ghcr.io/mstrhakr/audplexus:latest
```

Unraid-style variables:

```bash
docker run -d \
  --name audible-plex \
  -e PUID=99 \
  -e PGID=100 \
  -p 8080:8080 \
  -v /mnt/user/appdata/audible-plex/config:/config \
  -v /mnt/user/audiobooks:/audiobooks \
  -v /mnt/user/appdata/audible-plex/downloads:/downloads \
  ghcr.io/mstrhakr/audplexus:latest
```

Notes:

- If you pass `--user`, that identity is used directly.
- If you use `PUID`/`PGID`, the entrypoint drops privileges to that UID/GID.
- `TAKE_OWNERSHIP=true` can help when bind-mounted directories were created by another user.

## Docker Compose Example

The included `compose.yaml` uses the published image from GitHub Container Registry.

```yaml
services:
  audible-plex:
    image: ghcr.io/mstrhakr/audplexus:latest
    ports:
      - "8080:8080"
    volumes:
      - ./config:/config
      - /path/to/audiobooks:/audiobooks
      - ./downloads:/downloads
    environment:
      - DATABASE_TYPE=sqlite
    restart: unless-stopped
```

For PostgreSQL + health checks, use:

```bash
docker compose -f compose.postgres.yaml up -d
```

## Looking For Development Docs?

Developer and maintainer details were moved to `DEVELOPMENT.md`.

## License

MIT
