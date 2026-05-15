# Screenshot Naming

Use this naming format for every screenshot:

`{section}-{function}-{area}-{action}.png`

All filename rules:

- lowercase only
- words separated by `-`
- no spaces
- no dates/version numbers in names
- include specific setting or element in `area` when needed

Examples:

- `settings-config-output-format-select.png`
- `settings-config-sync-schedule-save.png`
- `library-details-book-actions-queue.png`
- `library-details-status-pill-complete.png`
- `destinations-create-plex-form-save.png`

## Token Meanings

- `section`: top-level doc area (`getting-started`, `settings`, `library`, `destinations`, `sync`, `downloads`, `diagnostics`)
- `function`: task inside that section (`config`, `create`, `run`, `queue`, `compare`, `targeted-scan`)
- `area`: specific UI panel/setting/element (`output-format`, `tag-profile`, `book-actions`, `phase-panel`)
- `action`: user action or view state (`open`, `select`, `edit`, `save`, `run`, `result`)

## Storage Location

Place screenshots here:

- `docs/user/screenshots/`

## Markdown Pattern

Use this markdown template in docs:

```md
![Short readable alt text](./screenshots/{section}-{function}-{area}-{action}.png)
```
