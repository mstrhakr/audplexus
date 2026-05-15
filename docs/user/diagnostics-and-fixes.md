# Diagnostics and Fixes

Use this page when books are missing or destination indexing looks wrong.

## Quick Checks

1. Confirm destination is enabled.
2. Run Test Connection on destination.
3. Verify destination library ID/section ID.
4. Run Full sync.

![Diagnostics page open](./screenshots/diagnostics-open-main-page-view.png)

## Compare Diagnostics

Open Diagnostics to compare what Audplexus has vs what destination reports.

Use this to spot:

- missing item on server
- path mismatch
- metadata match miss

![Diagnostics compare run](./screenshots/diagnostics-compare-controls-run.png)
![Diagnostics compare result table](./screenshots/diagnostics-compare-results-table.png)

## Targeted Scan

When one book is missing:

1. Open Diagnostics.
2. Run targeted scan for ASIN.
3. Pick destination(s).

![Diagnostics targeted scan open](./screenshots/diagnostics-targeted-scan-form-open.png)
![Diagnostics targeted scan run](./screenshots/diagnostics-targeted-scan-controls-run.png)
![Diagnostics targeted scan result](./screenshots/diagnostics-targeted-scan-result-banner.png)

Use after changing path mapping, library IDs, or moving files.

## If A Destination Keeps Failing

1. Disable that destination temporarily.
2. Keep other destinations enabled.
3. Fix credentials/path/server issue.
4. Re-enable and test connection.

## If Downloads Fail

1. Check free space and write permissions for downloads/audiobooks paths.
2. Retry failed item.
3. If repeated timeout/stall, retry later and check network path to Audible CDN.

