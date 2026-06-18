DROP INDEX IF EXISTS idx_users_oidc;
ALTER TABLE users DROP COLUMN auth_source;
ALTER TABLE users DROP COLUMN oidc_issuer;
ALTER TABLE users DROP COLUMN oidc_subject;
