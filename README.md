# Audible Plex Downloader

A pure Go Docker application that authenticates with Audible, downloads audiobooks, removes DRM via FFmpeg, fetches enriched metadata from Audnexus, and organizes files in Plex-compatible `Author/Title/Title.m4b` structure.

## Quick Start

### Using Pre-built Docker Image

```bash
# Pull the latest image
docker pull ghcr.io/mstrhakr/audible-plex-downloader:latest

# Create directories
mkdir -p config audiobooks downloads

# Run with Docker
docker run -d \
  --name audible-plex \
  -p 8080:8080 \
  --user 1000:1000 \
  -v $(pwd)/config:/config \
  -v $(pwd)/audiobooks:/audiobooks \
  -v $(pwd)/downloads:/downloads \
  ghcr.io/mstrhakr/audible-plex-downloader:latest

# Or use Docker Compose
docker compose up -d
```

### Building from Source

```bash
# Clone the repo
git clone https://github.com/mstrhakr/audible-plex-downloader.git
cd audible-plex-downloader

# Copy and edit config
cp config.example.yaml config/config.yaml

# Build and run
go build ./cmd/server
./audible-plex-downloader
```

Then visit `http://localhost:8080` to authenticate and manage your library.

## Configuration

Configuration can be provided via `config.yaml` or environment variables:

Precedence is:

1. DB-backed settings saved from the web UI (for runtime/user preferences)
2. Environment variables
3. `config.yaml` defaults

| Env Variable | Default | Description |
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

## Permission Handling

When the pipeline hits a filesystem permission error (for example writing to `/downloads` or moving files into `/audiobooks`), it now automatically pauses the queue to avoid repeatedly failing every remaining item.

- Current item is marked failed with the original error.
- Queue workers stop claiming new pending jobs.
- Resume from the Downloads page after fixing permissions.

### Running as a Different User

Use either standard Docker user mapping or Unraid-style `PUID`/`PGID`.

Standard Docker/Compose style:

```bash
docker run -d \
  --name audible-plex \
  --user 1000:1000 \
  -p 8080:8080 \
  -v $(pwd)/config:/config \
  -v $(pwd)/audiobooks:/audiobooks \
  -v $(pwd)/downloads:/downloads \
  ghcr.io/mstrhakr/audible-plex-downloader:latest
```

Unraid-style environment variables:

```bash
docker run -d \
  --name audible-plex \
  -e PUID=99 \
  -e PGID=100 \
  -p 8080:8080 \
  -v /mnt/user/appdata/audible-plex/config:/config \
  -v /mnt/user/audiobooks:/audiobooks \
  -v /mnt/user/appdata/audible-plex/downloads:/downloads \
  ghcr.io/mstrhakr/audible-plex-downloader:latest
```

Notes:

- If you pass `--user`, that identity is used directly.
- If you use `PUID`/`PGID`, the entrypoint drops privileges to that UID/GID.
- `TAKE_OWNERSHIP=true` can help when bind-mounted directories were created by another user.

## Docker Compose

The `compose.yaml` is configured to use the pre-built image from GitHub Container Registry:

```yaml
services:
  audible-plex:
    image: ghcr.io/mstrhakr/audible-plex-downloader:latest
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

For PostgreSQL with proper health checks, use `compose.postgres.yaml`:

```bash
docker compose -f compose.postgres.yaml up -d
```

### Available Docker Tags

When you tag a release like `v0.1.4`, the following images are automatically published:

- **Exact version:** `v0.1.4`, `0.1.4`
- **Floating minor:** `v0.1`, `0.1` (tracks latest patch in 0.1.x)
- **Floating major:** `v0`, `0` (tracks latest in 0.x.x)
- **Latest:** `latest` (latest release from main/master)
- **Branch:** `master`, `main` (latest commit on that branch)
- **Commit-specific:** `master-sha-abc123`

Example usage:

```bash
# Use latest stable release
docker pull ghcr.io/mstrhakr/audible-plex-downloader:latest

# Pin to major version (auto-updates to latest 0.x.x)
docker pull ghcr.io/mstrhakr/audible-plex-downloader:v0

# Pin to minor version (auto-updates to latest 0.1.x)
docker pull ghcr.io/mstrhakr/audible-plex-downloader:v0.1

# Pin to exact version (never changes)
docker pull ghcr.io/mstrhakr/audible-plex-downloader:v0.1.4
```

Images are automatically built and published via GitHub Actions on every push to main/master and on tagged releases.

### Building Docker Image Locally

Because this project depends on a local `go-audible` module, use the provided build scripts:

```bash
# Linux/macOS
./build-docker.sh

# Windows PowerShell
./build-docker.ps1
```

These scripts will set up the proper build context with both repositories and create an image tagged as `audible-plex-downloader:local`.

## Output Structure

Files are organized for Plex audiobook libraries:

```
/audiobooks/
  Author Name/
    Book Title/
      Book Title.m4b
      Book Title.chapters.txt
      cover.jpg
```

## Development

```bash
# Build locally
go build -o audible-plex-downloader ./cmd/server

# Run
./audible-plex-downloader
```

Requires Go 1.22+. Uses pure Go SQLite implementation (modernc.org/sqlite), so no CGO required.

## Releasing

To create a new release with automated binary builds and Docker images:

1. **Tag the release:**

  ```bash
  git commit --allow-empty -m "chore: release v0.1.4"
  git tag -a v0.1.4"
  git push origin v0.1.4
  ```

1. **Automated actions:**

- GitHub Actions builds binaries for Linux, macOS, and Windows (amd64 + arm64)
- Creates a GitHub Release with downloadable archives
- Builds and publishes Docker images with floating tags: `latest`, `v0`, `v0.1`, `v0.1.4`, `0`, `0.1`, `0.1.4`

All releases are available at: <https://github.com/mstrhakr/audible-plex-downloader/releases>

## License

MIT
