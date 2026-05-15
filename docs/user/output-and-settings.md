# Output and Settings

Use this page for output format, tags, and behavior defaults.

## Output Format

Choose:

- `m4b` (single audiobook file)
- `mp3` (converted output)

![Settings output format select](./screenshots/settings-config-output-format-select.png)

## Tag Profile

Choose in Settings -> Tag Profile.

- Basic: minimal metadata behavior
- Audiobook-rich: adds richer metadata such as series/asin for better downstream grouping

![Settings tag profile select](./screenshots/settings-config-tag-profile-select.png)

## Companion Output Options

Optional outputs include:

- chapter text file
- `.plexmatch` hint file
- embedded cover

![Settings chapter file toggle](./screenshots/settings-config-chapter-file-toggle.png)
![Settings plexmatch file toggle](./screenshots/settings-config-plexmatch-file-toggle.png)
![Settings embed cover toggle](./screenshots/settings-config-embed-cover-toggle.png)

## Important Paths

- Audiobooks path: final organized library root
- Downloads path: temporary working files
- Config path: app settings and auth data

![Settings paths section view](./screenshots/settings-config-paths-section-view.png)

## Configuration Priority

When settings come from multiple places, effective priority is:

1. Saved settings from web UI
2. Environment variables
3. `config.yaml`
4. Built-in defaults

