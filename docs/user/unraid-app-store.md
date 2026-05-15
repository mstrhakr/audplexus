# Unraid App Store Setup

Use this when you install Audplexus from Unraid Community Applications and want a quick container setup.

## Install From Community Applications

1. Open the Unraid web UI.
2. Go to Community Applications.
3. Search for Audplexus.
4. Install the container.
5. Set the image to the published Audplexus image if needed.

## Recommended Paths

Use these common mount points:

- `/mnt/user/appdata/audplexus/config` -> `/config`
- `/mnt/user/appdata/audplexus/downloads` -> `/downloads`
- `/mnt/user/audiobooks` -> `/audiobooks`

## User and Group Settings

If your Unraid setup uses custom permissions, set:

- `PUID` to your desired user ID
- `PGID` to your desired group ID

If you already manage permissions through the Unraid container settings, keep those values consistent with your existing apps.

## First Start

1. Start the container.
2. Open the web UI at `http://<unraid-ip>:8080`.
3. Connect Audible.
4. Add a library destination if you want automatic library scans after downloads.

## Notes

- Keep the `config` share backed up; it stores auth and app settings.
- If downloads fail with permission errors, check the mapped share permissions first.
- If you want step-by-step setup help after install, see [Getting Started](./getting-started.md).
