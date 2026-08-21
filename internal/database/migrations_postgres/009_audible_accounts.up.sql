-- Multiple Audible accounts. Mirrors the library_destinations model: one row
-- per connected Audible account, each with its own credentials + marketplace.
CREATE TABLE IF NOT EXISTS audible_accounts (
    id            TEXT PRIMARY KEY,
    display_name  TEXT NOT NULL DEFAULT '',
    marketplace   TEXT NOT NULL DEFAULT 'us',
    customer_id   TEXT NOT NULL DEFAULT '',
    credentials   BYTEA,
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE books ADD COLUMN IF NOT EXISTS account_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_books_account ON books(account_id);
