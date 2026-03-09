# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Docker support with automated GitHub Actions publishing to ghcr.io
- Multi-architecture Docker images (linux/amd64, linux/arm64)
- GitHub Actions workflow for automated releases
- Build scripts for local Docker builds (build-docker.sh, build-docker.ps1)
- Queue pause/resume controls in the web API/UI
- Docker runtime user mapping support for both `--user` and Unraid-style `PUID`/`PGID`

### Changed

- Pipeline "processing" stage renamed to "moving file" with real progress tracking
- Database upsert now properly updates status, file_path, and file_size for existing books

### Fixed

- Books already present in library no longer incorrectly show as "queued"
- File move progress now displays accurately instead of jumping to 70%
- Queue now auto-pauses when filesystem permission errors are detected, avoiding repeated failures

## [0.1.0] - YYYY-MM-DD

### Added

- Initial release
- Audible authentication and library sync
- Automated audiobook download and DRM removal
- Metadata enrichment via Audnexus API
- Plex-compatible file organization
- Web UI for library management
- SQLite and PostgreSQL database support
- FFmpeg integration for audio processing
- Multi-stage download pipeline (download → decrypt → move)
- Docker containerization
- Scheduled library sync

[Unreleased]: https://github.com/mstrhakr/audible-plex-downloader/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/mstrhakr/audible-plex-downloader/releases/tag/v0.1.0
