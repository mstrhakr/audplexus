# Queue and Download Books

Use this page to manage processing from queue to finished media.

## Queue Books

From Library or Dashboard:

- Queue single book
- Queue all new books

![Library queue single action](./screenshots/library-queue-book-actions-queue.png)
![Dashboard queue all new action](./screenshots/downloads-queue-controls-queue-all-new.png)

## Pipeline Stages

Each book moves through:

1. Downloading
2. Decrypting
3. Processing/Organizing
4. Destination fan-out (for enabled destinations)

![Downloads pipeline stage view](./screenshots/downloads-monitor-pipeline-stages-result.png)

## Pause, Resume, Cancel, Retry

Available actions:

- Pause downloads
- Resume downloads
- Cancel queued/active items
- Retry failed items

![Downloads pause action](./screenshots/downloads-manage-controls-pause.png)
![Downloads resume action](./screenshots/downloads-manage-controls-resume.png)
![Downloads cancel action](./screenshots/downloads-manage-item-cancel.png)
![Downloads retry failed action](./screenshots/downloads-manage-item-retry.png)

## Reorganize and Redownload

- Reorganize: re-run organization for existing media.
- Redownload: fetch source again for a specific ASIN when needed.

![Downloads redownload action](./screenshots/downloads-manage-item-redownload.png)

## Common Expectations

- A finished raw download is not final completion; decrypt/process still run.
- If one destination fails, other destinations can still complete.
- Progress is visible in Downloads and Dashboard panels.

