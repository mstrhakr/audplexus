# Audplexus

[![ghcr pulls](https://img.shields.io/badge/dynamic/json?url=https%3A%2F%2Fghcr-badge.elias.eu.org%2Fapi%2Fmstrhakr%2Faudplexus%2Faudplexus&query=downloadCount&label=ghcr+pulls&logo=github)](https://github.com/mstrhakr/audplexus/pkgs/container/audplexus/latest)
[![Tests](https://github.com/mstrhakr/audplexus/actions/workflows/tests.yml/badge.svg?branch=master)](https://github.com/mstrhakr/audplexus/actions/workflows/tests.yml)
[![Docker Build and Publish](https://github.com/mstrhakr/audplexus/actions/workflows/docker-publish.yml/badge.svg?branch=master)](https://github.com/mstrhakr/audplexus/actions/workflows/docker-publish.yml)
[![Latest Release](https://img.shields.io/github/v/release/mstrhakr/audplexus?display_name=tag)](https://github.com/mstrhakr/audplexus/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/mstrhakr/audplexus)](https://github.com/mstrhakr/audplexus/blob/master/go.mod)

Audplexus is a self-hosted app that syncs your Audible library, downloads audiobooks, and organizes them for your media server.

Use it when you want a single place to:

- connect Audible and keep your library in sync
- download books into a local audiobook library
- send finished books to Plex, Emby, Jellyfin, or Audiobookshelf
- keep the queue, retry, and sync flow in one web UI

## Quick Start

### Docker Compose

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

1. In Settings, connect Audible.
2. If you want automatic library scans, add a library destination.

### Docker Run

```bash
docker pull ghcr.io/mstrhakr/audplexus:latest

mkdir -p config audiobooks downloads

docker run -d \
	--name audplexus \
	-p 8080:8080 \
	-v $(pwd)/config:/config \
	-v $(pwd)/audiobooks:/audiobooks \
	-v $(pwd)/downloads:/downloads \
	ghcr.io/mstrhakr/audplexus:latest
```

If you run into permission issues, make sure the container user can write to `config/`, `audiobooks/`, and `downloads/`.

### Unraid / Community Applications

If you install from Unraid Community Applications, use the app entry as your container start point, map the common shares, and keep `PUID`/`PGID` aligned with your Unraid user setup.

Recommended share layout:

- `/mnt/user/appdata/audplexus/config` -> `/config`
- `/mnt/user/appdata/audplexus/downloads` -> `/downloads`
- `/mnt/user/audiobooks` -> `/audiobooks`

More details: [Unraid App Store Setup](docs/user/unraid-app-store.md)

## Basic Setup Notes

- Open the web UI at `http://localhost:8080`
- Connect Audible first
- Add a library destination if you want automatic scans after downloads
- Use the user guide for task-by-task help when you need more detail

## Common Paths

- `config/` stores app settings and auth data
- `downloads/` holds temporary work files
- `audiobooks/` is the final library output

Need more detail? Start with the [User Guide](docs/user/README.md) or [Developer Docs](docs/dev/README.md).

## Documentation

- [User Guide](docs/user/README.md)
- [Developer Docs](docs/dev/README.md)

## License

MIT
