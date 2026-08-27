# Audplexus Diagnostic Proxy Worker

This worker mirrors the Printmaster pattern:

1. Audplexus submits a diagnostics handoff report.
2. Worker optionally uploads a GitHub Gist.
3. Worker returns a prefilled GitHub issue URL.

## Files

- `src/worker.js`: Worker logic
- `wrangler.toml`: Worker deployment config

## One-time setup

1. Add `audplexus.dev` to Cloudflare.
2. Create DNS record:
- Type: `CNAME`
- Name: `api`
- Target: anything (e.g. `@`) and **proxied on** (orange cloud)
3. In Cloudflare Worker settings, set secret:
- `GITHUB_PAT` (token with gist permissions)

`GITHUB_REPO` is set in `wrangler.toml` (`mstrhakr/audplexus`).

## Automatic deploy from GitHub

This repo includes `.github/workflows/deploy-diagnostic-worker.yml`.

Set GitHub repository secrets:

- `CLOUDFLARE_API_TOKEN`
- `CLOUDFLARE_ACCOUNT_ID`

Then every push touching `cloudflare-worker/**` on `master` auto-deploys.

## Local deploy (optional)

```bash
cd cloudflare-worker
npx wrangler deploy
```

## Audplexus app default

Audplexus defaults to using:

- `https://api.audplexus.dev/diagnostic`

Override with server env var if needed:

- `AUDPLEXUS_DIAGNOSTIC_PROXY_URL`
