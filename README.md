# Audplexus

[![Tests](https://github.com/mstrhakr/audplexus/actions/workflows/tests.yml/badge.svg?branch=master)](https://github.com/mstrhakr/audplexus/actions/workflows/tests.yml)
[![Docker Build and Publish](https://github.com/mstrhakr/audplexus/actions/workflows/docker-publish.yml/badge.svg?branch=master)](https://github.com/mstrhakr/audplexus/actions/workflows/docker-publish.yml)
[![Latest Release](https://img.shields.io/github/v/release/mstrhakr/audplexus?display_name=tag)](https://github.com/mstrhakr/audplexus/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/mstrhakr/audplexus)](https://github.com/mstrhakr/audplexus/blob/master/go.mod)

A self-hosted web app that syncs your Audible library, downloads and processes audiobooks, and organizes output for supported media server libraries.

Built on [go-audible](https://github.com/mstrhakr/go-audible), the Audible auth and API client used for library sync, download metadata, and activation handling.

## What It Does

- Connects to Audible and syncs your library metadata.
- Downloads books and processes them into media-server-friendly output.
- Adds metadata and optional companion files (cover, chapters, match hints).
- Triggers media-server library scans and creates series collections automatically.
- Supports queue controls, retries, diagnostics, and scheduled sync.

## Media Server Integrations

Audplexus drives various media server software through a backend abstraction. Pick one with the `MEDIA_SERVER` env var (or via Settings &rarr; Media Server in the UI):

| Backend | `MEDIA_SERVER` | Status |
| --- | --- | --- |
| Plex | `plex` (default) | Full support: plex.tv OAuth login, section scans, `.plexmatch` hints, collection management |
| Emby | `emby` | Full support: API-key auth, library refresh, BoxSet collection management, automatic library-path detection |

Switching backends requires a container restart. The DB keeps backend settings, so flipping back is non-destructive.

### Emby Setup

1. Create an API key in Emby (Settings &rarr; Advanced &rarr; API Keys &rarr; New API Key).
2. Find your audiobook library&apos;s ItemId &mdash; either via the UI (Settings &rarr; Media Server &rarr; Emby panel will let you paste it), or via the API:

   ```bash
   curl -s "http://your-emby:8096/emby/Library/MediaFolders?api_key=YOUR_KEY" | jq '.Items[] | select(.CollectionType=="audiobooks") | {Id, Name}'
   ```

3. Set env vars (or fill in the Settings UI panel):

   ```bash
   MEDIA_SERVER=emby
   EMBY_URL=http://your-emby:8096
   EMBY_API_KEY=...
   EMBY_LIBRARY_ID=87111
   ```

4. After the first download (or via Settings &rarr; Trigger Test Library Refresh) Audplexus will:
   - Trigger a refresh of the configured library.
   - For each downloaded book, locate its item in Emby and add it to a BoxSet collection named after the series.
   - Run a periodic reconcile that walks Emby&apos;s library and ensures every series with matched books has a populated collection.

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

1. In Settings, connect Audible and (optionally) your media server.

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
| `AUDIOBOOKS_PATH` | `/audiobooks` | Output directory (media-server audiobook library root) |
| `DOWNLOADS_PATH` | `/downloads` | Temporary download directory |
| `CONFIG_PATH` | `/config` | Config/auth storage directory |
| `OUTPUT_FORMAT` | `m4b` | Output format (`m4b` or `mp3`) |
| `DOWNLOAD_CONCURRENCY` | `0` | Concurrent downloads (0 = auto-detect based on CPU) |
| `DECRYPT_CONCURRENCY` | `0` | Concurrent decrypt workers (0 = auto-detect) |
| `PROCESS_CONCURRENCY` | `0` | Concurrent process workers (0 = auto-detect) |
| `MEDIA_SERVER` | `plex` | Active backend for your media server integration |
| `PLEX_URL` | | Server URL for library scan triggers |
| `PLEX_TOKEN` | | Authentication token |
| `EMBY_URL` | | Server URL (e.g. `http://emby:8096`) |
| `EMBY_API_KEY` | | API key from `Settings &rarr; Advanced &rarr; API Keys` |
| `EMBY_LIBRARY_ID` | | `ItemId` of the audiobook library (`CollectionType=audiobooks`) |
| `EMBY_LIBRARY_PATH` | | Optional override of the path used to read the library; auto-detected via `VirtualFolders` on first scan |
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
