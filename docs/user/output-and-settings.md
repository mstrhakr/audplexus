# Output and Settings

This is the page to use when you want to control how Audplexus stores, tags, and organizes books.

## What this page controls

The output settings are where you choose the behavior that affects the final library and the downstream media-server experience. In practice, this is where you decide:

- which audio format to produce
- how much metadata to attach to the files
- whether companion files are created
- where the app stores final output and temporary work files
- how the app behaves when a book is newly discovered or reprocessed

## Output format

Choose the output style that matches your library goals:

- `m4b` for a single audiobook file with strong compatibility for media servers
- `mp3` for converted output when you want a more universal audio format

![Settings output format select](./screenshots/settings-config-output-format-select.png)

## Tag profile

Use the tag profile to tune metadata richness.

- Basic: minimal metadata behavior
- Audiobook-rich: adds richer metadata such as series information and ASIN-aware fields for better downstream grouping

![Settings tag profile select](./screenshots/settings-config-tag-profile-select.png)

## Companion output options

Optional outputs can help downstream tools and organization work more smoothly.

Common options include:

- chapter text files
- `.plexmatch` hint files
- embedded covers

![Settings chapter file toggle](./screenshots/settings-config-chapter-file-toggle.png)
![Settings plexmatch file toggle](./screenshots/settings-config-plexmatch-file-toggle.png)
![Settings embed cover toggle](./screenshots/settings-config-embed-cover-toggle.png)

## Important paths

Review these paths before you trust the final library layout:

- Audiobooks path: final organized library root
- Downloads path: temporary working files
- Config path: app settings and auth data

![Settings paths section view](./screenshots/settings-config-paths-section-view.png)

## Configuration priority

When settings are available in more than one place, the effective order is:

1. saved settings from the web UI
2. environment variables
3. config file values
4. built-in defaults

This matters most when you are debugging a mismatch between what you expect and what the app is doing.

## Safe defaults

If you are not sure what to choose:

- keep the audiobook output format aligned with your destination system
- prefer the richer tag profile if you use Plex, Jellyfin, or Audiobookshelf heavily
- validate one test book before changing a large library path or file format choice

