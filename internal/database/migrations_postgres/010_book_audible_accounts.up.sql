-- A book can exist in more than one connected Audible account's library.
-- books.account_id keeps the FIRST owner (download routing + legacy), this
-- junction records EVERY owner so the UI can show/filter "which account(s)
-- is this from".
CREATE TABLE IF NOT EXISTS book_audible_accounts (
    asin       TEXT NOT NULL,
    account_id TEXT NOT NULL REFERENCES audible_accounts(id) ON DELETE CASCADE,
    PRIMARY KEY (asin, account_id)
);
CREATE INDEX IF NOT EXISTS idx_baa_account ON book_audible_accounts(account_id);

-- Backfill from the single-owner column so existing rows filter correctly.
INSERT INTO book_audible_accounts (asin, account_id)
    SELECT asin, account_id FROM books
    WHERE account_id != ''
      AND account_id IN (SELECT id FROM audible_accounts)
ON CONFLICT DO NOTHING;
