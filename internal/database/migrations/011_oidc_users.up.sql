-- OIDC/OAuth identity on the users table. A user is either forms-backed
-- (password/salt/iterations set, auth_source='forms') or OIDC-backed
-- (oidc_subject/oidc_issuer set, password empty, auth_source='oidc').
ALTER TABLE users ADD COLUMN oidc_subject TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN oidc_issuer  TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN auth_source  TEXT NOT NULL DEFAULT 'forms';

-- One local user per (issuer, subject). Partial index so the many forms
-- users (all with empty subject) don't collide.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_oidc
  ON users(oidc_issuer, oidc_subject)
  WHERE oidc_subject <> '';
