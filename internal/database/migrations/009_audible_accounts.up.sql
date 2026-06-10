-- Multiple Audible accounts. Mirrors the library_destinations model: one row
-- per connected Audible account, each with its own credentials + marketplace.
--
-- Most installs have exactly one account. First-boot synthesis creates a single
-- row from the legacy credentials.json so existing users are untouched.
CREATE TABLE IF NOT EXISTS audible_accounts (
    id            TEXT PRIMARY KEY,
    display_name  TEXT NOT NULL DEFAULT '',
    marketplace   TEXT NOT NULL DEFAULT 'us',
    -- Stable Audible customer id (from credentials). Lets us match a re-auth
    -- back to the same account and dedupe accidental double-adds.
    customer_id   TEXT NOT NULL DEFAULT '',
    -- Full audible.Credentials JSON blob. Same shape as credentials.json.
    credentials   BLOB,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Owning account for each book. NULL/'' means "unknown / legacy single-account"
-- and resolves to the default account at download time.
ALTER TABLE books ADD COLUMN account_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_books_account ON books(account_id);
