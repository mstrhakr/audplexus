# Sync Your Library

Sync updates Audplexus metadata from Audible and refreshes library visibility.

## Sync Modes

### Quick Sync

Use when:

- you recently bought books
- you want a fast metadata refresh

### Full Sync

Use when:

- destination libraries are out of date
- metadata/path matching seems wrong
- you want a full refresh pass

## Run Sync Manually

1. Open Dashboard.
2. Start Quick or Full sync.
3. Watch phase progress.

![Dashboard sync quick run](./screenshots/sync-run-controls-quick-run.png)
![Dashboard sync full run](./screenshots/sync-run-controls-full-run.png)

## Phase Progress You Will See

- Audible Library
- File System Scan
- Library Scan
- Collection Sync

You may also see per-destination progress when more than one destination is configured.

![Dashboard sync phase panel](./screenshots/sync-monitor-phase-panel-result.png)
![Dashboard sync destination subphase](./screenshots/sync-monitor-destination-progress-result.png)

## Scheduled Sync

Set in Settings:

- cron schedule
- mode (quick/full)
- auto-queue new books after scheduled sync

![Settings sync schedule edit](./screenshots/settings-config-sync-schedule-edit.png)
![Settings sync mode select](./screenshots/settings-config-sync-mode-select.png)
![Settings sync auto queue save](./screenshots/settings-config-sync-auto-queue-save.png)

## If Sync Says Already Running

Only one sync can run at a time. Wait for current run to finish, then retry.

