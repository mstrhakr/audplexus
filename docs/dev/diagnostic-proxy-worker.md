# Audplexus Diagnostic Proxy Worker

This worker powers the Audplexus diagnostics handoff flow:

1. Audplexus posts a diagnostics report JSON to a Cloudflare Worker.
2. Worker creates a GitHub Gist (optional, based on upload mode).
3. Worker returns a pre-filled GitHub issue URL.

## Endpoint

- Path: `/diagnostic`
- Method: `POST`
- Content type: `application/json`

Request shape:

```json
{
  "report": {
    "report_id": "AUD-20260826-153000",
    "timestamp": "2026-08-26T15:30:00Z",
    "issue_type": "sync",
    "expected_value": "All purchased books should appear",
    "user_message": "Book missing after full sync",
    "issue_title": "diagnostics: sync issue",
    "range": "24h",
    "mode": "package",
    "detail": "full",
    "upload_mode": "gist_secret",
    "runtime_env": {},
    "env_presence": {},
    "destinations": [],
    "recent_logs": []
  }
}
```

Response shape:

```json
{
  "success": true,
  "gist_url": "https://gist.github.com/...",
  "issue_url": "https://github.com/<owner>/<repo>/issues/new?..."
}
```

## Worker Configuration

Set in Cloudflare Worker environment:

- `GITHUB_PAT`: token with gist write access
- `GITHUB_REPO`: repository slug, e.g. `mstrhakr/audplexus`

Current worker behavior does not call the GitHub Issues API directly. It returns
a pre-filled issue URL to open in the browser.

## Audplexus Configuration

Set server env var to point at the worker:

- `AUDPLEXUS_DIAGNOSTIC_PROXY_URL=https://api.audplexus.dev/diagnostic`

If unset, Audplexus defaults to:

- `https://api.audplexus.dev/diagnostic`

## Notes

- "Secret" gists are unlisted, not private in a strict security sense.
- Keep sanitization in Audplexus before report submission.
- The worker is intentionally simple and stateless.
