# Audplexus

[![ghcr pulls](https://img.shields.io/badge/dynamic/json?url=https%3A%2F%2Fghcr-badge.elias.eu.org%2Fapi%2Fmstrhakr%2Faudplexus%2Faudplexus&query=downloadCount&label=ghcr+pulls&logo=github)](https://github.com/mstrhakr/audplexus/pkgs/container/audplexus/latest)
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

## Library Destinations

Audplexus pushes each downloaded book to one or more **library destinations** &mdash; the servers where your audiobook library lives. Multiple destinations of the same type are allowed (household Plex + parents&apos; Plex; or Emby for the home + ABS for the phone).

| Backend | Type | What Audplexus does |
| --- | --- | --- |
| Plex | `plex` | Section scan triggers, `.plexmatch` hints, collection management for series |
| Emby | `emby` | Library refresh, BoxSet collection management, series + franchise tagging, BoxSet covers, author images |
| Jellyfin | `jellyfin` | Same shape as Emby with the proper `Authorization: MediaBrowser Token` header and `IncludeItemTypes=AudioBook` filter |
| Audiobookshelf | `abs` | Bearer-token auth, library scan trigger, ASIN-based item matching, native series via metadata |

After each download finishes Audplexus fans out post-organize work concurrently to every enabled destination (bounded to 3 in flight, 2-min per-destination timeout) and records per-(book, destination) state in `book_library_destinations`. One destination&apos;s outage doesn&apos;t prevent the others from indexing the new book.

### Adding a destination

In the web UI: **Settings &rarr; Library Destinations &rarr; Add destination**. Pick a type, fill in URL + API key (or Plex token) + library ID, click **Test Connection** to verify, then **Save**. Per-field instructions (where to find each value) live in the form itself.

For headless setup, env vars at first boot synthesize one destination of the matching type:

```bash
# Plex
MEDIA_SERVER=plex   # optional; inferred from the variables below
PLEX_URL=http://plex.lan:32400
PLEX_TOKEN=...
PLEX_SECTION_ID=5

# Emby
MEDIA_SERVER=emby
EMBY_URL=http://emby.lan:8096
EMBY_API_KEY=...
EMBY_LIBRARY_ID=87111

# Jellyfin
MEDIA_SERVER=jellyfin
JELLYFIN_URL=http://jellyfin.lan:8096
JELLYFIN_API_KEY=...
JELLYFIN_LIBRARY_ID=...

# Audiobookshelf
MEDIA_SERVER=abs
ABS_URL=http://abs.lan
ABS_API_KEY=...   # admin-scope token from Settings &rarr; Users &rarr; API Keys
ABS_LIBRARY_ID=...
```

After first boot, additional destinations are added through the web UI; `MEDIA_SERVER` becomes a deprecated bootstrap shim.

### Tag profiles

The **Audiobook-rich** tag profile (Settings &rarr; Tag Profile) writes `series`, `series-part`, and `asin` freeform iTunes atoms into each m4b. Audiobookshelf reads these via `ffprobe` for native series auto-detection &mdash; no additional API calls required to group books into series. Default is **Basic** (preserves v0.2.x behavior); opt in once and every subsequent download writes the richer set.

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
| `MEDIA_SERVER` | `plex` | First-boot synthesis hint &mdash; type of the destination to create from per-type env vars on a fresh install. Ignored once any destination exists in the DB. |
| `PLEX_URL` | | Server URL for library scan triggers |
| `PLEX_TOKEN` | | Authentication token |
| `EMBY_URL` | | Server URL (e.g. `http://emby:8096`) |
| `EMBY_API_KEY` | | API key from `Settings &rarr; Advanced &rarr; API Keys` |
| `EMBY_LIBRARY_ID` | | `ItemId` of the audiobook library (`CollectionType=audiobooks`) |
| `EMBY_LIBRARY_PATH` | | Optional override of the path used to read the library; auto-detected via `VirtualFolders` on first scan |
| `SYNC_SCHEDULE` | `0 */6 * * *` | Cron schedule for library sync |
| `SYNC_MODE` | `full` | Scheduled sync mode (`quick` or `full`) |
| `SYNC_AUTO_QUEUE_NEW` | `false` | Automatically append newly discovered books (`new` status) to the download queue after sync |
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
