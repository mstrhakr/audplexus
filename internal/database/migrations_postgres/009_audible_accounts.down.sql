DROP INDEX IF EXISTS idx_books_account;
ALTER TABLE books DROP COLUMN IF EXISTS account_id;
DROP TABLE IF EXISTS audible_accounts;
