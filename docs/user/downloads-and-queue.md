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

Example using synthetic book data. Stage labels and colored card edges distinguish active work; each card shows its own progress and available actions.

Mobile layout:

![Pipeline cards with wrapped titles and controls on mobile](./screenshots/downloads-monitor-pipeline-mobile-result.png)

## Dashboard Completions

The dashboard defaults to the last seven days and lists the ten newest
completions in that window. Choose 24 hours, seven days, 30 days, or all time.
The matching total includes entries beyond the ten shown; **View history** opens
the full Pipeline history for the selected window.

Older entries leave the dashboard as they age out of the selected window, but
remain in download history. Automatic dashboard refreshes preserve your selection.

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
