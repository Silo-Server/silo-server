-- +goose Up
-- The server Owner is the account that claimed the server at first-run setup.
-- Only the Owner may change its own account; other admins may not edit,
-- disable, delete, reset, or act as it. At most one account is the Owner, and
-- it stays an enabled admin.
ALTER TABLE users ADD COLUMN is_owner boolean NOT NULL DEFAULT false;
ALTER TABLE users ADD CONSTRAINT users_owner_is_enabled_admin
    CHECK (NOT is_owner OR (role = 'admin' AND enabled));
CREATE UNIQUE INDEX users_single_owner_key ON users ((true)) WHERE is_owner;

-- Existing servers: the earliest-created enabled admin is the Owner.
UPDATE users SET is_owner = true
WHERE id = (
    SELECT id FROM users
    WHERE role = 'admin' AND enabled
    ORDER BY created_at, id
    LIMIT 1
);

-- +goose Down
DROP INDEX users_single_owner_key;
ALTER TABLE users DROP CONSTRAINT users_owner_is_enabled_admin;
ALTER TABLE users DROP COLUMN is_owner;
