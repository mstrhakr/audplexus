# Connect a Library Destination

Use this when you want Audplexus to push finished books into your media server library.

Supported destination types:

- Plex
- Emby
- Jellyfin
- Audiobookshelf

## Add a Destination in the Web UI

1. Open Settings -> Library Destinations.
2. Select Add destination.
3. Pick destination type.
4. Fill required fields.
5. Select Test Connection.
6. Save.

Repeat for each server/library you want. Multiple destinations of the same type are supported.

![Destinations list add button](./screenshots/destinations-create-list-add.png)
![Destinations type picker select](./screenshots/destinations-create-type-picker-select.png)

## Required Values by Type

### Plex

- Server URL
- Token
- Section ID (audiobook library section)

![Destinations plex form save](./screenshots/destinations-create-plex-form-save.png)

### Emby

- Server URL
- API key
- Library ID

![Destinations emby form save](./screenshots/destinations-create-emby-form-save.png)

### Jellyfin

- Server URL
- API key
- Library ID

![Destinations jellyfin form save](./screenshots/destinations-create-jellyfin-form-save.png)

### Audiobookshelf

- Server URL
- API key
- Library ID

![Destinations abs form save](./screenshots/destinations-create-abs-form-save.png)

## Check Connection Health

Each destination has a Test Connection action.

Use it when:

- first adding a destination
- changing URL/token/key/library ID
- troubleshooting missing books

![Destinations test connection result](./screenshots/destinations-health-test-connection-result.png)

## Enable or Disable a Destination

Disable a destination when a server is down or being migrated.

Behavior:

- Disabled destination is skipped.
- Other enabled destinations continue processing.

![Destinations toggle enabled state](./screenshots/destinations-manage-enable-toggle.png)

## Path Mapping (When Server Cannot See the Same Path)

If media server reads a different mount path than Audplexus:

1. Set Audiobook Path (source path seen by Audplexus).
2. Set Destination Path (target path seen by the destination server).

This helps targeted scans and matching use the correct server-visible path.

![Destinations path mapping edit](./screenshots/destinations-config-path-mapping-save.png)

