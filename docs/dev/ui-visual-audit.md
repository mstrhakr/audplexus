# UI Visual Audit

Completion-order fix: `fa41734`. This audit covers the subsequent pipeline, responsive-layout, and settings-control fixes. Release testing of connected integrations remains separate.

## Findings Addressed

| Priority | Finding | Change |
| --- | --- | --- |
| Medium | Initial pipeline cards used a different class from live cards, so snapshots could leave stale cards behind. | Initial and live cards now share the same markup/class recognized by snapshot cleanup. |
| Medium | Mobile pipeline badges and actions squeezed book titles to a few characters. | Titles wrap on their own row on narrow screens. |
| Medium | Removing the final active item left a blank pipeline. | Both snapshots and live transitions restore the empty state. |
| Medium | Diagnostics logs, long environment paths, and Drift Inbox widened mobile pages. | Controls wrap, grid tracks can shrink, paths wrap, and the issue table scrolls within its container. |
| Medium | Mobile book details remained in two columns because an obsolete flex rule did not affect CSS Grid. | Mobile details use one grid column; metadata values wrap. |
| Medium | The file-logging toggle label overlapped its helper text. | The control now uses the same toggle markup as neighboring settings. |
| Low | Live-added cards added bottom margins on top of the list gap. | The list owns spacing; card margins no longer add a second gap. |
| Low | Pipeline items were difficult to distinguish visually. | Consistent card boundaries and stage-colored edges complement the text badges. |
| Low | Restore-wizard actions extended beyond the panel on mobile. | Wizard footer actions wrap. |

## Verification

- `go test ./internal/web -count=1` passed after the final code edits.
- Server build passed.
- Whitespace checks passed with `cr-at-eol`, preserving the CRLF format already tracked for CSS and base/settings templates.
- Playwright captured 60 audit screenshots at 1440 x 1000 and 390 x 844. Only two curated pipeline crops are retained in the documentation; the full audit gallery is a local artifact.
- All audited page routes returned HTTP 200; no page-level JavaScript errors or broken images were reported.
- Audited pages did not overflow the viewport horizontally. Wide diagnostic tables scroll locally.
- Pipeline snapshots removed stale initial cards. Add/remove transitions maintained approximately 8.8px gaps and restored the empty state after the last completion.

## Coverage Limits

The audit used an isolated SQLite database with synthetic books and simulated SSE/snapshot events. Production data and configuration were not changed. Audible sign-in, connected-account/device states, media-server discovery, HearthShelf approval, actual downloads, backup restoration, and destructive actions were not exercised. Screenshots are visual evidence, not end-to-end verification of those integrations.

## Retained Screenshots

- [Desktop pipeline](../user/screenshots/downloads-monitor-pipeline-stages-result.png): active-stage examples used in the README and download guide.
- [Mobile pipeline](../user/screenshots/downloads-monitor-pipeline-mobile-result.png): title wrapping and stage/action layout in the download guide.

Both crops use synthetic book data and omit settings, credentials, and fixture-dependent global counters. Keep the remaining audit captures local rather than publishing an exhaustive gallery or adding stale screenshot links.
